package storage

import "testing"

func TestObjectKey(t *testing.T) {
	cases := []struct {
		assetID string
		variant AssetVariant
		ext     string
		want    string
	}{
		{"asset_abc", VariantHigh, "png", "assets/asset_abc/high.png"},
		{"asset_abc", VariantLow, "png", "assets/asset_abc/low.png"},
		{"asset_abc", VariantThumb, ".png", "assets/asset_abc/thumb.png"},
	}
	for _, c := range cases {
		got := ObjectKey(c.assetID, c.variant, c.ext)
		if got != c.want {
			t.Errorf("ObjectKey(%q, %q, %q) = %q, want %q", c.assetID, c.variant, c.ext, got, c.want)
		}
	}
}

func TestCanonicalURL(t *testing.T) {
	got := CanonicalURL("my-bucket", "assets/asset_abc/high.png")
	want := "s3://my-bucket/assets/asset_abc/high.png"
	if got != want {
		t.Errorf("CanonicalURL = %q, want %q", got, want)
	}
}
