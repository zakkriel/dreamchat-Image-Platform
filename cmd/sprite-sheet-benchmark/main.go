// Command sprite-sheet-benchmark is the Wave 4 gate-1 evidence tool.
//
// It is standalone measurement tooling, NOT platform runtime: nothing in the
// API or the worker imports it, and running it changes no platform state. It
// drives a real provider adapter through the ordinary providers.ImageProvider
// interface, slices each returned sheet deterministically, and records what the
// release gates in docs/superpowers/specs/2026-08-08-wave4-amortization-design.md
// need to be decided against numbers instead of impressions.
//
// It deliberately measures only what a machine can measure honestly: pane
// decodability, geometry, blankness, latency, and provider-reported cost.
// Identity consistency and pane separation are human judgements; the tool writes
// the sliced artifacts and leaves those fields empty for a reviewer. It never
// synthesizes a quality score, and it never fabricates a result when a provider
// is unreachable or unconfigured.
//
// Usage:
//
//	FAL_KEY=... go run ./cmd/sprite-sheet-benchmark \
//	  -provider fal -anchor https://... -rows 2 -cols 2 \
//	  -prompt "neutral portrait, three-quarter view" \
//	  -cell-keys neutral,warm,serious,surprised \
//	  -samples 30 -single-cost-usd 0.0100 -out ./benchmark-out
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "image/jpeg"

	"github.com/zakkriel/drchat-image-platform/internal/providers"
	"github.com/zakkriel/drchat-image-platform/internal/providers/bfl"
	"github.com/zakkriel/drchat-image-platform/internal/providers/fal"
	"github.com/zakkriel/drchat-image-platform/internal/providers/mock"
)

// cellReport is the per-pane structural verdict. Every field here is machine
// decidable; nothing in it expresses an opinion about how the image looks.
type cellReport struct {
	Key      string `json:"cell_key"`
	Row      int    `json:"row"`
	Column   int    `json:"column"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Decoded  bool   `json:"decoded"`
	Blank    bool   `json:"blank"`
	Usable   bool   `json:"usable"`
	File     string `json:"file,omitempty"`
	Rejected string `json:"rejection,omitempty"`
}

// sampleRecord is one benchmark sample: exactly one parent sheet call.
type sampleRecord struct {
	Sample            int          `json:"sample"`
	Provider          string       `json:"provider"`
	Model             string       `json:"model"`
	ProviderRequestID string       `json:"provider_request_id"`
	ProviderJobID     string       `json:"provider_job_id"`
	Rows              int          `json:"rows"`
	Columns           int          `json:"columns"`
	AnchorURLs        []string     `json:"anchor_urls"`
	RequestedWidth    int          `json:"requested_width"`
	RequestedHeight   int          `json:"requested_height"`
	DecodedWidth      int          `json:"decoded_width"`
	DecodedHeight     int          `json:"decoded_height"`
	ReportedWidth     int          `json:"reported_width"`
	ReportedHeight    int          `json:"reported_height"`
	LatencyMs         int64        `json:"latency_ms"`
	ProviderCostUSD   string       `json:"provider_cost_usd"`
	SheetFile         string       `json:"sheet_file,omitempty"`
	Malformed         string       `json:"malformed,omitempty"`
	ContentRejected   bool         `json:"content_rejected"`
	Error             string       `json:"error,omitempty"`
	Cells             []cellReport `json:"cells"`

	// Reviewer fields. The tool never fills these in - gate 2's identity
	// consistency and pane separation are human judgements made against the
	// written artifacts.
	IdentityConsistency string `json:"identity_consistency_reviewer"`
	PaneSeparation      string `json:"pane_separation_reviewer"`
	CrossPaneBleed      string `json:"cross_pane_bleed_reviewer"`
}

type summary struct {
	Provider           string `json:"provider"`
	Model              string `json:"model"`
	Grid               string `json:"grid"`
	Samples            int    `json:"samples"`
	SheetsAttempted    int    `json:"sheets_attempted"`
	SheetsMalformed    int    `json:"sheets_malformed"`
	ContentRejections  int    `json:"content_rejections"`
	CallFailures       int    `json:"call_failures"`
	CellsDeclared      int    `json:"cells_declared"`
	CellsUsable        int    `json:"cells_usable"`
	UsablePaneRate     string `json:"usable_pane_rate"`
	ReportedCostUSD    string `json:"reported_cost_usd"`
	CostPerUsableUSD   string `json:"cost_per_usable_image_usd"`
	SingleBaselineUSD  string `json:"single_image_baseline_usd,omitempty"`
	BaselineRatio      string `json:"cost_ratio_vs_singles,omitempty"`
	CostEvidenceIsFull bool   `json:"cost_evidence_complete"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "sprite-sheet-benchmark:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		providerID = flag.String("provider", "fal", "provider adapter to drive: fal, bfl, or mock")
		anchors    = flag.String("anchor", "", "comma-separated anchor/reference image URLs (required by reference-conditioned providers)")
		rows       = flag.Int("rows", 2, "sheet row count")
		cols       = flag.Int("cols", 2, "sheet column count")
		prompt     = flag.String("prompt", "", "prompt for the parent sheet call (required)")
		cellKeys   = flag.String("cell-keys", "", "comma-separated cell keys, exactly rows*cols entries (required)")
		samples    = flag.Int("samples", 30, "number of sheets to render (gate 1 requires >= 30 per provider/model/grid)")
		outDir     = flag.String("out", "./benchmark-out", "directory for sliced panes and JSON records")
		width      = flag.Int("width", 2048, "requested sheet width in pixels")
		height     = flag.Int("height", 2048, "requested sheet height in pixels")
		singleCost = flag.String("single-cost-usd", "", "measured provider cost of ONE single image, for the gate-3 comparison")
		timeout    = flag.Duration("timeout", 10*time.Minute, "per-sample timeout")
	)
	flag.Parse()

	if *rows < 1 || *cols < 1 {
		return fmt.Errorf("rows and cols must both be >= 1, got %dx%d", *rows, *cols)
	}
	if strings.TrimSpace(*prompt) == "" {
		return errors.New("-prompt is required")
	}
	keys := splitList(*cellKeys)
	if len(keys) != *rows**cols {
		return fmt.Errorf("-cell-keys must list exactly rows*cols = %d keys, got %d", *rows**cols, len(keys))
	}
	if dup := firstDuplicate(keys); dup != "" {
		return fmt.Errorf("-cell-keys must be unique, %q repeats", dup)
	}
	if *samples < 1 {
		return fmt.Errorf("-samples must be >= 1, got %d", *samples)
	}

	adapter, err := adapterFor(*providerID)
	if err != nil {
		return err
	}
	caps := adapter.Capabilities()
	anchorURLs := splitList(*anchors)
	if caps.RequiresReferenceImage && len(anchorURLs) == 0 {
		return fmt.Errorf("provider %q is reference-conditioned: -anchor must supply at least one reachable image URL", *providerID)
	}
	for _, a := range anchorURLs {
		if !strings.HasPrefix(a, "http://") && !strings.HasPrefix(a, "https://") {
			return fmt.Errorf("-anchor %q must be a URL the provider can fetch; this tool does not upload local files", a)
		}
	}
	if *samples < 30 {
		fmt.Fprintf(os.Stderr, "warning: %d samples is below the gate-1 minimum of 30; results are not admissible evidence\n", *samples)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	recordsPath := filepath.Join(*outDir, "records.jsonl")
	recordsFile, err := os.Create(recordsPath)
	if err != nil {
		return fmt.Errorf("create records file: %w", err)
	}
	defer recordsFile.Close()
	encoder := json.NewEncoder(recordsFile)

	sum := summary{
		Provider:           *providerID,
		Model:              caps.ModelName,
		Grid:               fmt.Sprintf("%dx%d", *rows, *cols),
		Samples:            *samples,
		CostEvidenceIsFull: true,
	}
	totalCost := new(big.Rat)

	for i := 1; i <= *samples; i++ {
		rec := renderSample(context.Background(), adapter, sampleInput{
			index:      i,
			providerID: *providerID,
			model:      caps.ModelName,
			prompt:     *prompt,
			anchors:    anchorURLs,
			rows:       *rows,
			cols:       *cols,
			keys:       keys,
			width:      *width,
			height:     *height,
			outDir:     *outDir,
			timeout:    *timeout,
		})
		if err := encoder.Encode(rec); err != nil {
			return fmt.Errorf("write record: %w", err)
		}
		accumulate(&sum, totalCost, rec, len(keys))
		fmt.Printf("sample %d/%d: %s\n", i, *samples, describe(rec, len(keys)))
	}

	finish(&sum, totalCost, *singleCost)
	summaryPath := filepath.Join(*outDir, "summary.json")
	blob, err := json.MarshalIndent(sum, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(summaryPath, append(blob, '\n'), 0o644); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}

	fmt.Printf("\n%s\n", blob)
	fmt.Printf("\nrecords: %s\nsummary: %s\n", recordsPath, summaryPath)
	fmt.Println("\nGate 2 identity consistency and pane separation are NOT measured here.")
	fmt.Println("A reviewer must judge the written panes by eye and fill in the")
	fmt.Println("*_reviewer fields in records.jsonl before the gate decision is recorded.")
	if !sum.CostEvidenceIsFull {
		fmt.Println("\nWARNING: at least one sample carried no provider-reported cost.")
		fmt.Println("Gate 3 requires provider actuals; estimated price is not evidence.")
	}
	return nil
}

type sampleInput struct {
	index      int
	providerID string
	model      string
	prompt     string
	anchors    []string
	rows, cols int
	keys       []string
	width      int
	height     int
	outDir     string
	timeout    time.Duration
}

// renderSample performs exactly ONE parent-sheet provider call. A failure is
// recorded and the run moves to the next sample: the benchmark never retries a
// call and never re-routes, so a content-policy rejection is counted verbatim
// rather than worked around.
func renderSample(parent context.Context, adapter providers.ImageProvider, in sampleInput) sampleRecord {
	rec := sampleRecord{
		Sample:          in.index,
		Provider:        in.providerID,
		Model:           in.model,
		Rows:            in.rows,
		Columns:         in.cols,
		AnchorURLs:      in.anchors,
		RequestedWidth:  in.width,
		RequestedHeight: in.height,
	}

	ctx, cancel := context.WithTimeout(parent, in.timeout)
	defer cancel()

	started := time.Now()
	res, err := adapter.Generate(ctx, providers.ProviderGenerateRequest{
		JobID:         fmt.Sprintf("benchmark_%d", in.index),
		Operation:     providers.OperationTextToImage,
		Prompt:        in.prompt,
		Width:         in.width,
		Height:        in.height,
		ReferenceURLs: in.anchors,
		Metadata: map[string]any{
			"benchmark": "sprite_sheet",
			"rows":      in.rows,
			"cols":      in.cols,
		},
	})
	rec.LatencyMs = time.Since(started).Milliseconds()

	if err != nil {
		rec.Error = err.Error()
		rec.ContentRejected = errors.Is(err, providers.ErrContentPolicyRejected)
		return rec
	}
	rec.ProviderJobID = res.ProviderJobID
	rec.ProviderRequestID = res.ProviderRequestID
	if res.ActualCostUSD != nil {
		rec.ProviderCostUSD = *res.ActualCostUSD
	}
	if len(res.Images) == 0 {
		rec.Malformed = "provider returned no images"
		return rec
	}

	img := res.Images[0]
	rec.ReportedWidth, rec.ReportedHeight = img.Width, img.Height
	if len(img.Bytes) == 0 {
		rec.Malformed = "provider returned no image bytes"
		return rec
	}
	// Provider-declared dimensions are provenance. Decode the bytes.
	decoded, _, decErr := image.Decode(bytes.NewReader(img.Bytes))
	if decErr != nil {
		rec.Malformed = "sheet did not decode: " + decErr.Error()
		return rec
	}
	bounds := decoded.Bounds()
	rec.DecodedWidth, rec.DecodedHeight = bounds.Dx(), bounds.Dy()
	if bounds.Dx() < in.cols || bounds.Dy() < in.rows {
		rec.Malformed = fmt.Sprintf("decoded sheet %dx%d is too small for a %dx%d grid",
			bounds.Dx(), bounds.Dy(), in.rows, in.cols)
		return rec
	}

	sheetPath := filepath.Join(in.outDir, fmt.Sprintf("sample_%03d_sheet.png", in.index))
	if writeErr := writePNG(sheetPath, decoded); writeErr == nil {
		rec.SheetFile = sheetPath
	}
	rec.Cells = sliceSheet(decoded, in)
	return rec
}

// sliceSheet cuts the sheet into equal cells in row-major order. The division is
// deterministic and integer-floored, so the same sheet always yields the same
// panes and a reviewer can reproduce the cut.
func sliceSheet(sheet image.Image, in sampleInput) []cellReport {
	bounds := sheet.Bounds()
	cellW := bounds.Dx() / in.cols
	cellH := bounds.Dy() / in.rows
	cropper, canCrop := sheet.(interface {
		SubImage(image.Rectangle) image.Image
	})

	reports := make([]cellReport, 0, len(in.keys))
	for row := range in.rows {
		for col := range in.cols {
			key := in.keys[row*in.cols+col]
			report := cellReport{Key: key, Row: row, Column: col, Width: cellW, Height: cellH}
			rect := image.Rect(
				bounds.Min.X+col*cellW, bounds.Min.Y+row*cellH,
				bounds.Min.X+(col+1)*cellW, bounds.Min.Y+(row+1)*cellH,
			)
			if !canCrop {
				report.Rejected = "sheet image does not support sub-imaging"
				reports = append(reports, report)
				continue
			}
			cell := cropper.SubImage(rect)
			if cell.Bounds().Dx() <= 0 || cell.Bounds().Dy() <= 0 {
				report.Rejected = "empty cell geometry"
				reports = append(reports, report)
				continue
			}
			report.Decoded = true
			report.Blank = isUniform(cell)
			// A pane is usable only when it decodes, has the declared geometry,
			// and carries actual image content. A flat fill is the common shape
			// of "the provider ignored the grid" and is never a usable image.
			report.Usable = !report.Blank
			if report.Blank {
				report.Rejected = "pane is a single flat colour"
			}
			path := filepath.Join(in.outDir, fmt.Sprintf("sample_%03d_%s.png", in.index, sanitize(key)))
			if err := writePNG(path, cell); err != nil {
				report.Usable = false
				report.Rejected = "could not write pane: " + err.Error()
			} else {
				report.File = path
			}
			reports = append(reports, report)
		}
	}
	return reports
}

// isUniform reports whether every pixel in the image is the same colour, which
// is how an ignored-grid or padding pane presents.
func isUniform(img image.Image) bool {
	b := img.Bounds()
	r0, g0, b0, a0 := img.At(b.Min.X, b.Min.Y).RGBA()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			if r != r0 || g != g0 || bl != b0 || a != a0 {
				return false
			}
		}
	}
	return true
}

func accumulate(sum *summary, totalCost *big.Rat, rec sampleRecord, declaredCells int) {
	sum.SheetsAttempted++
	sum.CellsDeclared += declaredCells
	switch {
	case rec.ContentRejected:
		sum.ContentRejections++
	case rec.Error != "":
		sum.CallFailures++
	case rec.Malformed != "":
		sum.SheetsMalformed++
	}
	for _, c := range rec.Cells {
		if c.Usable {
			sum.CellsUsable++
		}
	}
	// A call that never reached the provider is not missing cost evidence.
	billable := rec.Error == "" || rec.ContentRejected
	if rec.ProviderCostUSD != "" {
		if v, ok := new(big.Rat).SetString(rec.ProviderCostUSD); ok {
			totalCost.Add(totalCost, v)
			return
		}
	}
	if billable {
		sum.CostEvidenceIsFull = false
	}
}

func finish(sum *summary, totalCost *big.Rat, singleCost string) {
	sum.ReportedCostUSD = totalCost.FloatString(6)
	if sum.CellsDeclared > 0 {
		rate := new(big.Rat).SetFrac64(int64(sum.CellsUsable), int64(sum.CellsDeclared))
		sum.UsablePaneRate = rate.FloatString(4)
	}
	if sum.CellsUsable == 0 {
		sum.CostPerUsableUSD = "n/a (no usable panes)"
		return
	}
	perUsable := new(big.Rat).Quo(totalCost, new(big.Rat).SetInt64(int64(sum.CellsUsable)))
	sum.CostPerUsableUSD = perUsable.FloatString(6)

	baseline, ok := new(big.Rat).SetString(strings.TrimSpace(singleCost))
	if !ok || baseline.Sign() <= 0 {
		return
	}
	sum.SingleBaselineUSD = baseline.FloatString(6)
	sum.BaselineRatio = new(big.Rat).Quo(perUsable, baseline).FloatString(4)
}

func describe(rec sampleRecord, declaredCells int) string {
	switch {
	case rec.ContentRejected:
		return "provider content rejection (recorded verbatim, not retried): " + rec.Error
	case rec.Error != "":
		return "call failed: " + rec.Error
	case rec.Malformed != "":
		return "malformed sheet: " + rec.Malformed
	}
	usable := 0
	for _, c := range rec.Cells {
		if c.Usable {
			usable++
		}
	}
	return fmt.Sprintf("%dx%d sheet, %d/%d usable panes, %dms, cost %s",
		rec.DecodedWidth, rec.DecodedHeight, usable, declaredCells, rec.LatencyMs, orNone(rec.ProviderCostUSD))
}

// adapterFor builds a real adapter from the environment. There is deliberately
// no offline or synthetic fallback for a real provider: a benchmark that
// invents results is worse than no benchmark.
func adapterFor(providerID string) (providers.ImageProvider, error) {
	switch providerID {
	case fal.ProviderID:
		key := os.Getenv("FAL_KEY")
		if key == "" {
			return nil, errors.New("FAL_KEY is not set: cannot benchmark fal without real credentials")
		}
		return fal.New(key), nil
	case bfl.ProviderID:
		key := os.Getenv("BFL_API_KEY")
		if key == "" {
			return nil, errors.New("BFL_API_KEY is not set: cannot benchmark bfl without real credentials")
		}
		return bfl.New(key), nil
	case mock.ProviderID:
		fmt.Fprintln(os.Stderr, "warning: the mock provider is synthetic; its output is NOT gate evidence")
		return mock.New(), nil
	default:
		return nil, fmt.Errorf("unknown provider %q (want fal, bfl, or mock)", providerID)
	}
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func splitList(raw string) []string {
	out := []string{}
	for _, part := range strings.Split(raw, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func firstDuplicate(values []string) string {
	seen := make(map[string]struct{}, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			return v
		}
		seen[v] = struct{}{}
	}
	return ""
}

func sanitize(key string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, key)
}

func orNone(v string) string {
	if v == "" {
		return "not reported"
	}
	return v
}
