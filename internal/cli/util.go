package cli

import (
	"os"

	"gopkg.in/yaml.v3"
)

func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}

func yamlUnmarshal(data []byte, v any) error { return yaml.Unmarshal(data, v) }
