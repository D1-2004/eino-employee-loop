package dws

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderConvertsGatewayMessages(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "fake-dws.sh")
	script := "#!/bin/sh\necho '{\"messages\":[{\"messageId\":\"m1\",\"sender\":\"Ann\",\"senderId\":\"a\",\"text\":\"hi\"}]}'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p := NewDWSProvider(DWSConfig{DWSBin: bin})
	hist, err := p.FetchHistory(context.Background(), "cid", 5)
	if err != nil || len(hist) != 1 || hist[0].MsgID != "m1" || hist[0].Speaker != "Ann" || hist[0].OperatorID != "a" || hist[0].Kind != "" {
		t.Fatalf("history %+v err %v", hist, err)
	}
	if !NegativeReaction("thumbsdown", "") || NegativeReaction("thumbsup", "") {
		t.Fatal("lexicon delegates to the gateway")
	}
}
