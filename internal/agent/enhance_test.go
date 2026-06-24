package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/everva/conductor-platform/internal/agentclient"
)

// fakeEnhanceGateway scripts the enhance agent-API for EnhanceRunner tests.
type fakeEnhanceGateway struct {
	claim   *agentclient.EnhanceClaim // nil → nothing pending
	claimed bool

	// recorded
	gotResult string
	gotErr    string
	completed bool
}

func (f *fakeEnhanceGateway) ClaimEnhance(_ context.Context, _ string) (agentclient.EnhanceClaim, bool, error) {
	if f.claim == nil {
		return agentclient.EnhanceClaim{}, false, nil
	}
	c := *f.claim
	f.claim = nil // claim once
	f.claimed = true
	return c, true, nil
}

func (f *fakeEnhanceGateway) CompleteEnhance(_ context.Context, _, _, result, errMsg string) error {
	f.completed = true
	f.gotResult = result
	f.gotErr = errMsg
	return nil
}

// fakeEnhancer scripts the code-aware enhance.
type fakeEnhancer struct {
	spec string
	err  error
	runs int
}

func (f *fakeEnhancer) Enhance(_ context.Context, _, _ string) (string, error) {
	f.runs++
	return f.spec, f.err
}

func TestEnhanceRunOnce_NoJob(t *testing.T) {
	gw := &fakeEnhanceGateway{claim: nil}
	ex := &fakeEnhancer{}
	r := NewEnhanceRunner(gw, ex, "p", 0, nil)
	handled, err := r.RunOnce(context.Background())
	if err != nil || handled {
		t.Fatalf("no job → handled=false,nil; got handled=%v err=%v", handled, err)
	}
	if ex.runs != 0 || gw.completed {
		t.Fatalf("no job must not enhance or complete")
	}
}

func TestEnhanceRunOnce_Success(t *testing.T) {
	gw := &fakeEnhanceGateway{claim: &agentclient.EnhanceClaim{ID: "enh-1", RoughSpec: "servis şirketini kaldır"}}
	ex := &fakeEnhancer{spec: "## Detaylı Türkçe spec\n- ..."}
	r := NewEnhanceRunner(gw, ex, "p", 0, nil)
	handled, err := r.RunOnce(context.Background())
	if err != nil || !handled {
		t.Fatalf("job → handled=true,nil; got handled=%v err=%v", handled, err)
	}
	if ex.runs != 1 {
		t.Fatalf("enhance must run exactly once, got %d", ex.runs)
	}
	if !gw.completed || gw.gotResult != ex.spec || gw.gotErr != "" {
		t.Fatalf("must complete with the spec and no error: %+v", gw)
	}
}

// A failing enhance is reported back as a FAILED job (never silently dropped, never fake-success).
func TestEnhanceRunOnce_FailureReported(t *testing.T) {
	gw := &fakeEnhanceGateway{claim: &agentclient.EnhanceClaim{ID: "enh-2", RoughSpec: "x"}}
	ex := &fakeEnhancer{err: errors.New("claude exploded")}
	r := NewEnhanceRunner(gw, ex, "p", 0, nil)
	handled, err := r.RunOnce(context.Background())
	if err != nil || !handled {
		t.Fatalf("handled job (even failed) → handled=true,nil; got handled=%v err=%v", handled, err)
	}
	if !gw.completed || gw.gotResult != "" || gw.gotErr == "" {
		t.Fatalf("failure must complete the job with an error, no result: %+v", gw)
	}
}
