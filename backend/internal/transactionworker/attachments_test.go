package transactionworker

import (
	"reflect"
	"testing"
)

func TestImageConversionCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		goos        string
		wantCommand string
		wantArgs    []string
	}{
		{
			name:        "Darwin uses sips",
			goos:        "darwin",
			wantCommand: "sips",
			wantArgs:    []string{"-s", "format", "png", "/tmp/source.heic", "--out", "/tmp/converted.png"},
		},
		{
			name:        "Linux uses ImageMagick",
			goos:        "linux",
			wantCommand: "magick",
			wantArgs:    []string{"/tmp/source.heic", "-strip", "/tmp/converted.png"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			command, args := imageConversionCommand(test.goos, "/tmp/source.heic", "/tmp/converted.png")
			if command != test.wantCommand {
				t.Fatalf("command = %q, want %q", command, test.wantCommand)
			}
			if !reflect.DeepEqual(args, test.wantArgs) {
				t.Fatalf("args = %#v, want %#v", args, test.wantArgs)
			}
		})
	}
}
