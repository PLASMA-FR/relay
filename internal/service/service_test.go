package service

import (
	"github.com/PLASMA-FR/relay/internal/config"
	"strings"
	"testing"
)

func TestUnitQuotesPaths(t *testing.T) {
	c := config.Default()
	c.Paths.ConfigFile = "/tmp/a b/%x/config.toml"
	s := Unit("/tmp/a b/relay", c)
	if !strings.Contains(s, `ExecStart="/tmp/a b/relay" --config "/tmp/a b/%%x/config.toml" daemon`) {
		t.Fatal(s)
	}
	if strings.Contains(s, "sudo") || !strings.Contains(s, "UMask=0077") {
		t.Fatal(s)
	}
}
