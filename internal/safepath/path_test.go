package safepath

import "testing"

func TestCleanRequestPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{name: "image", raw: "/images/test.jpg", want: "/images/test.jpg"},
		{name: "missing slash", raw: "css/app.css", want: "/css/app.css"},
		{name: "video", raw: "/videos/clip.mp4", want: "/videos/clip.mp4"},
		{name: "traversal", raw: "/images/../secret.txt", wantErr: true},
		{name: "encoded traversal", raw: "/images/%2e%2e/secret.txt", wantErr: true},
		{name: "unsupported", raw: "/private/key.txt", wantErr: true},
		{name: "empty", raw: "/", wantErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := CleanRequestPath(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
