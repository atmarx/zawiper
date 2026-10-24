// Package config reads /etc/zawiper/bay.toml: flat `key = "value"` lines.
// The file is root-owned and written once, when a person provisions the bay.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

func Read(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cfg := map[string]string{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s:%d: expected key = \"value\"", path, n)
		}
		v = strings.TrimSpace(v)
		if uq, err := strconv.Unquote(v); err == nil {
			v = uq
		}
		cfg[strings.TrimSpace(k)] = v
	}
	if cfg["port"] == "" {
		return nil, fmt.Errorf("%s: no port", path)
	}
	if cfg["name"] == "" {
		cfg["name"] = "the bay"
	}
	return cfg, sc.Err()
}
