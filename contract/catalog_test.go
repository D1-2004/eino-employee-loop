package contract

import (
	"errors"
	"testing"
	"time"
)

func TestStandardBusinessTypeConstantsAreCatalogued(t *testing.T) {
	c := StandardCatalog()
	want := map[string]Mode{TypeMessageAddressed: ModeTriggered, TypeMessageObserved: ModeListening, TypeReactionAdded: ModeTriggered}
	for typ, mode := range want {
		s, ok := c.Lookup(typ)
		if !ok || s.Mode != mode || s.Class != ClassBusiness {
			t.Errorf("%s: %+v ok=%v", typ, s, ok)
		}
	}
}

func TestStandardCatalogCoversEveryClassAndMode(t *testing.T) {
	c := StandardCatalog()
	classes := map[Class]bool{}
	modes := map[Mode]bool{}
	for _, s := range c.Types() {
		classes[s.Class] = true
		modes[s.Mode] = true
		if s.Mode == ModeListening && s.Lane == LaneForeground {
			t.Fatalf("%s: listening event in the foreground", s.Type)
		}
	}
	for _, cl := range Classes {
		if !classes[cl] {
			t.Errorf("no standard type for class %s", cl)
		}
	}
	if !modes[ModeTriggered] || !modes[ModeListening] {
		t.Errorf("both modes must be represented: %v", modes)
	}
}

func TestTypeSpecRejectsListeningForeground(t *testing.T) {
	err := TypeSpec{Type: "x", Class: ClassBusiness, Mode: ModeListening, Lane: LaneForeground, Session: SessionInteractive, RequiresScene: true, SummaryKey: "k"}.Validate()
	if err == nil {
		t.Fatal("listening events must not enter the foreground")
	}
	err = TypeSpec{Type: "x", Class: ClassCompletion, Mode: ModeTriggered, Lane: LaneForeground, Session: SessionInteractive, RequiresScene: true, SummaryKey: "k"}.Validate()
	if err == nil {
		t.Fatal("completion events must not enter the foreground")
	}
}

func event(typ, scene string, actor ActorKind, refs map[string]string) Event {
	return Event{ID: typ + "-1", Source: "test", Type: typ, Scene: SceneRef{ID: scene, Kind: SceneGroup}, Actor: Actor{ID: "u1", Kind: actor}, OccurredAt: time.Unix(1, 0), Refs: refs}
}

func TestRouteAddressedMessagePreemptsObservation(t *testing.T) {
	adm, err := StandardCatalog().Route(event("message.addressed", "s1", ActorHuman, nil))
	if err != nil {
		t.Fatal(err)
	}
	if adm.Session == nil || adm.Session.Kind != SessionInteractive || adm.Spec.Lane != LaneForeground {
		t.Fatalf("unexpected route %+v", adm)
	}
	if len(adm.Cancel) != 1 || adm.Cancel[0].Kind != SessionObservation || !adm.Cancel[0].OnlyPreemp || adm.Cancel[0].Reason != CancelForegroundAdmission {
		t.Fatalf("addressed messages preempt observation: %+v", adm.Cancel)
	}
	if adm.Event.Class != ClassBusiness || adm.Event.Mode != ModeTriggered {
		t.Fatalf("class/mode filled from the catalog: %+v", adm.Event)
	}
}

func TestRouteFollowUpSessionIsKeyedByTask(t *testing.T) {
	c := StandardCatalog()
	if _, err := c.Route(event("execution.result", "s1", ActorSystem, nil)); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("missing task ref must be rejected, got %v", err)
	}
	adm, err := c.Route(event("execution.result", "s1", ActorSystem, map[string]string{RefTask: "t9"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := adm.Session.String(); got != "s1/follow_up/t9" {
		t.Fatalf("session %s", got)
	}
}

func TestRouteRejectsUnknownTypesAndMismatches(t *testing.T) {
	c := StandardCatalog()
	if _, err := c.Route(event("nope", "s1", ActorHuman, nil)); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("unknown type: %v", err)
	}
	e := event("message.observed", "s1", ActorHuman, nil)
	e.Mode = ModeTriggered
	if _, err := c.Route(e); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("mode mismatch: %v", err)
	}
	if _, err := c.Route(event("message.addressed", "s1", ActorSystem, nil)); !errors.Is(err, ErrInvalidEvent) {
		t.Fatalf("foreground needs a human: %v", err)
	}
	if _, err := c.Route(event("message.addressed", "", ActorHuman, nil)); !errors.Is(err, ErrUnknownScene) {
		t.Fatalf("missing scene: %v", err)
	}
}

func TestRouteEnablementRevokesWithoutScene(t *testing.T) {
	adm, err := StandardCatalog().Route(event("subscription.changed", "", ActorSystem, nil))
	if err != nil {
		t.Fatal(err)
	}
	if adm.Session != nil || len(adm.Cancel) != 1 || adm.Cancel[0].Kind != SessionObservation || adm.Cancel[0].Scene != "" {
		t.Fatalf("subscription change revokes observation in every scene: %+v", adm)
	}
}

func TestStandardToolsValidate(t *testing.T) {
	tools, err := StandardTools()
	if err != nil {
		t.Fatal(err)
	}
	fg := ToolsFor(tools, LaneForeground, SessionInteractive)
	obs := ToolsFor(tools, LaneBackground, SessionObservation)
	has := func(xs []ToolSpec, name string) bool {
		for _, x := range xs {
			if x.Name == name {
				return true
			}
		}
		return false
	}
	if !has(fg, "dispatch_task") || has(obs, "dispatch_task") {
		t.Fatal("dispatch is foreground-only")
	}
	if !has(obs, "reply") || !has(obs, "stay_quiet") {
		t.Fatal("observation can reply or stay quiet")
	}
}

func TestFingerprintIgnoresDerivedFields(t *testing.T) {
	e := event("message.addressed", "s1", ActorHuman, nil)
	f := e
	f.Class, f.Mode = ClassBusiness, ModeTriggered
	if e.Fingerprint() != f.Fingerprint() {
		t.Fatal("class/mode are derived from the catalog and must not change the fingerprint")
	}
	f.Text = "changed"
	if e.Fingerprint() == f.Fingerprint() {
		t.Fatal("content changes the fingerprint")
	}
}

// The standard catalogs hold only what the module implements: no
// platform flow (plans, collections, reminders, engine switches) and no
// task parameter that no Task Manager of the module honours.
func TestStandardCatalogHoldsOnlyWhatTheModuleImplements(t *testing.T) {
	std := StandardCatalog()
	for _, typ := range []string{"plan.step_completed", "collection.ready", "task.reminder", "engine.changed"} {
		if _, ok := std.Lookup(typ); ok {
			t.Errorf("%s is not implemented by the module and must not be standard", typ)
		}
	}
	tools, err := StandardTools()
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range tools {
		switch s.Name {
		case "continue_plan", "describe_capabilities", "scene_config_get", "create_collection", "set_scene_participation":
			t.Errorf("%s is a platform tool, not a standard one", s.Name)
		}
	}
	for _, s := range tools {
		if s.Name != "dispatch_task" {
			continue
		}
		props, _ := s.Parameters["properties"].(map[string]any)
		for _, p := range []string{"builds_on", "follow_up_steps"} {
			if _, ok := props[p]; ok {
				t.Errorf("dispatch_task advertises %s, which no Task Manager of the module implements", p)
			}
		}
	}
}
