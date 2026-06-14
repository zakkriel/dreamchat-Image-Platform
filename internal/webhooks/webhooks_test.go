package webhooks

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeRepo is an in-memory Repository for unit tests (no DB). It keys endpoints
// by tenant and id, deliveries by id, and records the last MarkDeliveryResult.
type fakeRepo struct {
	endpointByTenant map[string]Endpoint
	endpointByID     map[string]Endpoint
	deliveries       map[string]Delivery
	inserted         []Delivery
	results          []DeliveryResult
	insertErr        error
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		endpointByTenant: map[string]Endpoint{},
		endpointByID:     map[string]Endpoint{},
		deliveries:       map[string]Delivery{},
	}
}

func (f *fakeRepo) withEndpoint(e Endpoint) *fakeRepo {
	f.endpointByTenant[e.TenantID] = e
	f.endpointByID[e.ID] = e
	return f
}

func (f *fakeRepo) GetActiveEndpointByTenant(_ context.Context, tenantID string) (Endpoint, error) {
	e, ok := f.endpointByTenant[tenantID]
	if !ok {
		return Endpoint{}, ErrNotFound
	}
	return e, nil
}

func (f *fakeRepo) GetEndpointByID(_ context.Context, id string) (Endpoint, error) {
	e, ok := f.endpointByID[id]
	if !ok {
		return Endpoint{}, ErrNotFound
	}
	return e, nil
}

func (f *fakeRepo) InsertEndpoint(_ context.Context, p InsertEndpointParams) (Endpoint, error) {
	e := Endpoint{ID: p.ID, TenantID: p.TenantID, URL: p.URL, Secret: p.Secret, IsActive: true}
	f.withEndpoint(e)
	return e, nil
}

func (f *fakeRepo) UpdateEndpointURL(_ context.Context, id, url string) (Endpoint, error) {
	e, ok := f.endpointByID[id]
	if !ok {
		return Endpoint{}, ErrNotFound
	}
	e.URL = url
	f.withEndpoint(e)
	return e, nil
}

func (f *fakeRepo) InsertDelivery(_ context.Context, p InsertDeliveryParams) (Delivery, error) {
	if f.insertErr != nil {
		return Delivery{}, f.insertErr
	}
	d := Delivery{
		ID:                p.ID,
		TenantID:          p.TenantID,
		WebhookEndpointID: p.WebhookEndpointID,
		EventType:         p.EventType,
		GenerationJobID:   p.GenerationJobID,
		Payload:           p.Payload,
		Status:            StatusPending,
	}
	f.deliveries[d.ID] = d
	f.inserted = append(f.inserted, d)
	return d, nil
}

func (f *fakeRepo) GetDeliveryByID(_ context.Context, id string) (Delivery, error) {
	d, ok := f.deliveries[id]
	if !ok {
		return Delivery{}, ErrNotFound
	}
	return d, nil
}

func (f *fakeRepo) MarkDeliveryResult(_ context.Context, res DeliveryResult) error {
	f.results = append(f.results, res)
	return nil
}

// fakeEnqueuer records enqueued delivery ids without Redis.
type fakeEnqueuer struct {
	enqueued []string
}

func (f *fakeEnqueuer) EnqueueDeliver(_ context.Context, deliveryID string) error {
	f.enqueued = append(f.enqueued, deliveryID)
	return nil
}

func (f *fakeEnqueuer) Close() error { return nil }

func TestEmitNoOpWhenNoEndpoint(t *testing.T) {
	repo := newFakeRepo() // no endpoint for the tenant
	enq := &fakeEnqueuer{}
	em := &Emitter{Repo: repo, Enqueuer: enq}

	if err := em.Emit(context.Background(), EmitInput{
		TenantID:  "tenant_a",
		EventType: EventCompleted,
		JobID:     "job_1",
		Data:      map[string]any{"final_asset_ids": []string{"asset_1"}},
	}); err != nil {
		t.Fatalf("Emit returned error on no endpoint: %v", err)
	}
	if len(repo.inserted) != 0 {
		t.Fatalf("expected no delivery inserted, got %d", len(repo.inserted))
	}
	if len(enq.enqueued) != 0 {
		t.Fatalf("expected no enqueue, got %d", len(enq.enqueued))
	}
}

func TestEmitInsertsAndEnqueuesWhenEndpointExists(t *testing.T) {
	repo := newFakeRepo().withEndpoint(Endpoint{
		ID: "whe_1", TenantID: "tenant_a", URL: "https://example.test/hook", Secret: "sec", IsActive: true,
	})
	enq := &fakeEnqueuer{}
	em := &Emitter{Repo: repo, Enqueuer: enq}

	if err := em.Emit(context.Background(), EmitInput{
		TenantID:  "tenant_a",
		EventType: EventCompleted,
		JobID:     "job_1",
		Data:      map[string]any{"final_asset_ids": []string{"asset_1"}},
	}); err != nil {
		t.Fatalf("Emit returned error: %v", err)
	}
	if len(repo.inserted) != 1 {
		t.Fatalf("expected 1 delivery inserted, got %d", len(repo.inserted))
	}
	got := repo.inserted[0]
	if got.WebhookEndpointID != "whe_1" || got.EventType != EventCompleted {
		t.Fatalf("unexpected delivery: %+v", got)
	}
	if len(enq.enqueued) != 1 || enq.enqueued[0] != got.ID {
		t.Fatalf("expected enqueue of delivery id %q, got %v", got.ID, enq.enqueued)
	}
	// The persisted payload is the signed/sent event JSON.
	var ev Event
	if err := json.Unmarshal(got.Payload, &ev); err != nil {
		t.Fatalf("payload is not valid Event JSON: %v", err)
	}
	if ev.Event != EventCompleted || ev.JobID != "job_1" || ev.TenantID != "tenant_a" {
		t.Fatalf("unexpected event payload: %+v", ev)
	}
}

func TestEmitNilEnqueuerNoOps(t *testing.T) {
	repo := newFakeRepo().withEndpoint(Endpoint{ID: "whe_1", TenantID: "tenant_a", URL: "https://x.test", Secret: "s"})
	em := &Emitter{Repo: repo, Enqueuer: nil}
	if err := em.Emit(context.Background(), EmitInput{TenantID: "tenant_a", EventType: EventCompleted}); err != nil {
		t.Fatalf("Emit with nil enqueuer must no-op, got %v", err)
	}
	if len(repo.inserted) != 0 {
		t.Fatalf("nil enqueuer must not insert a delivery")
	}
}

func TestDelivererSuccessMarksDeliveredAndSigns(t *testing.T) {
	const secret = "whsec_abc"
	payload := []byte(`{"event":"generation_job.completed","tenant_id":"tenant_a"}`)

	var gotSig, gotEvent, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotSig = r.Header.Get(headerSignature)
		gotEvent = r.Header.Get(headerEvent)
		gotCT = r.Header.Get("Content-Type")
		// Server-side signature verification over the exact received bytes.
		if Sign(secret, body) != gotSig {
			t.Errorf("server-side signature mismatch: header=%s", gotSig)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	repo := newFakeRepo().withEndpoint(Endpoint{ID: "whe_1", TenantID: "tenant_a", URL: srv.URL, Secret: secret})
	repo.deliveries["whd_1"] = Delivery{
		ID: "whd_1", TenantID: "tenant_a", WebhookEndpointID: "whe_1",
		EventType: EventCompleted, Payload: payload, Status: StatusPending,
	}
	d := NewDeliverer(repo, srv.Client(), nil)

	if err := d.Handle(context.Background(), "whd_1"); err != nil {
		t.Fatalf("Handle returned error on 2xx: %v", err)
	}
	if len(repo.results) != 1 || repo.results[0].Status != StatusDelivered {
		t.Fatalf("expected one delivered result, got %+v", repo.results)
	}
	if gotEvent != EventCompleted {
		t.Fatalf("expected event header %q, got %q", EventCompleted, gotEvent)
	}
	if gotCT != "application/json" {
		t.Fatalf("expected application/json content-type, got %q", gotCT)
	}
	if gotSig != Sign(secret, payload) {
		t.Fatalf("signature header mismatch")
	}
}

func TestDelivererNon2xxMarksFailedAndReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	repo := newFakeRepo().withEndpoint(Endpoint{ID: "whe_1", TenantID: "tenant_a", URL: srv.URL, Secret: "s"})
	repo.deliveries["whd_1"] = Delivery{
		ID: "whd_1", TenantID: "tenant_a", WebhookEndpointID: "whe_1",
		EventType: EventFailed, Payload: []byte(`{"event":"generation_job.failed"}`), Status: StatusPending,
	}
	d := NewDeliverer(repo, srv.Client(), nil)

	err := d.Handle(context.Background(), "whd_1")
	if err == nil {
		t.Fatal("expected Handle to return an error on 500 so asynq retries")
	}
	if len(repo.results) != 1 || repo.results[0].Status != StatusFailed {
		t.Fatalf("expected one failed result, got %+v", repo.results)
	}
	if repo.results[0].HTTPStatus != http.StatusInternalServerError {
		t.Fatalf("expected last_http_status 500, got %d", repo.results[0].HTTPStatus)
	}
}
