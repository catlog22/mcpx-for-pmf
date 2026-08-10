package main

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"mcpx/internal/observation"
)

func TestWorkspaceTUIUsesSixtyFPS(t *testing.T) {
	if workspaceTUIFramesPerSecond != 60 {
		t.Fatalf("workspace TUI fps=%d, want 60", workspaceTUIFramesPerSecond)
	}
	if scrollSpringDampingRatio != 1 {
		t.Fatalf("scroll damping=%v, want critically damped spring", scrollSpringDampingRatio)
	}
}

func TestWorkspaceTUIViewEnablesAltScreenAndMouse(t *testing.T) {
	model := newWorkspaceTUIModel("demo", observation.NewTextRenderer(false), false)
	model.setSize(80, 12)
	view := model.View()
	if !view.AltScreen {
		t.Fatal("TUI must use alternate screen")
	}
	if view.MouseMode != tea.MouseModeCellMotion {
		t.Fatalf("mouse mode=%v, want cell motion", view.MouseMode)
	}
	for _, want := range []string{"MCPX ARC · demo", "FOLLOW", "PgUp/PgDn", "Home/End", "wheel", "select", "Ctrl+C copy"} {
		if !strings.Contains(view.Content, want) {
			t.Fatalf("view missing %q: %q", want, view.Content)
		}
	}
}

func TestWorkspaceTUIKeyboardNavigationUsesSpringTargets(t *testing.T) {
	model := populatedWorkspaceTUIModel(t, 40)
	bottom := model.viewport.YOffset()
	if !model.viewport.AtBottom() || !model.autoFollow {
		t.Fatal("fixture must start following bottom")
	}
	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyPgUp}))
	if model.viewport.YOffset() != bottom || model.autoFollow || !model.scrollAnimating || model.scrollTarget >= float64(bottom) {
		t.Fatalf("page up must start animated scroll: offset=%d target=%.2f follow=%v animating=%v", model.viewport.YOffset(), model.scrollTarget, model.autoFollow, model.scrollAnimating)
	}
	settleWorkspaceScroll(t, model)
	pageOffset := model.viewport.YOffset()
	if pageOffset >= bottom {
		t.Fatalf("page up did not settle above bottom: bottom=%d after=%d", bottom, pageOffset)
	}
	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	settleWorkspaceScroll(t, model)
	if model.viewport.YOffset() >= pageOffset {
		t.Fatalf("up key did not scroll upward: before=%d after=%d", pageOffset, model.viewport.YOffset())
	}
	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	settleWorkspaceScroll(t, model)
	if !model.viewport.AtTop() || model.autoFollow {
		t.Fatalf("home must settle at top without follow: offset=%d", model.viewport.YOffset())
	}
	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	if !model.autoFollow || !model.scrollAnimating {
		t.Fatal("end must immediately restore follow intent and animate toward bottom")
	}
	settleWorkspaceScroll(t, model)
	if !model.viewport.AtBottom() || !model.autoFollow {
		t.Fatalf("end must settle at bottom follow: offset=%d", model.viewport.YOffset())
	}
}

func TestWorkspaceTUIMouseWheelStartsSmoothVirtualScroll(t *testing.T) {
	model := populatedWorkspaceTUIModel(t, 40)
	bottom := model.viewport.YOffset()
	_, _ = model.Update(tea.MouseWheelMsg(tea.Mouse{Button: tea.MouseWheelUp, X: 1, Y: 4}))
	if model.viewport.YOffset() != bottom || model.autoFollow || !model.scrollAnimating {
		t.Fatalf("wheel up must start animation without jumping: before=%d after=%d follow=%v animating=%v", bottom, model.viewport.YOffset(), model.autoFollow, model.scrollAnimating)
	}
	for range 6 {
		model.stepScrollAnimation()
	}
	if model.viewport.YOffset() >= bottom {
		t.Fatalf("spring frames did not move viewport upward: bottom=%d after=%d", bottom, model.viewport.YOffset())
	}
	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnd}))
	settleWorkspaceScroll(t, model)
	if !model.viewport.AtBottom() || !model.autoFollow {
		t.Fatal("end must restore follow after animated mouse scrolling")
	}
}

func TestWorkspaceTUIFollowAnimatesNewEventsInsteadOfJumping(t *testing.T) {
	model := populatedWorkspaceTUIModel(t, 40)
	before := model.viewport.YOffset()
	cmd, err := model.consumeFrame(testWorkspaceTUIEvent(100, "new.go"))
	if err != nil {
		t.Fatal(err)
	}
	if cmd == nil || !model.scrollAnimating || !model.autoFollow {
		t.Fatalf("new event in follow mode must schedule spring animation: cmd=%v animating=%v follow=%v", cmd != nil, model.scrollAnimating, model.autoFollow)
	}
	if model.viewport.YOffset() != before {
		t.Fatalf("new event must not jump immediately: before=%d after=%d", before, model.viewport.YOffset())
	}
	if model.scrollTarget <= float64(before) {
		t.Fatalf("follow target did not advance: before=%d target=%.2f", before, model.scrollTarget)
	}
	settleWorkspaceScroll(t, model)
	if !model.viewport.AtBottom() {
		t.Fatalf("follow animation did not settle at bottom: offset=%d max=%d", model.viewport.YOffset(), model.maxYOffset())
	}
}

func TestWorkspaceTUIDragSelectsWithoutCopyingOnRelease(t *testing.T) {
	model := populatedWorkspaceTUIModel(t, 40)
	line := model.viewport.YOffset()
	want := fmt.Sprintf("line-%02d", line+1)
	_, _ = model.Update(tea.MouseClickMsg(tea.Mouse{Button: tea.MouseLeft, X: 0, Y: model.viewport.YPosition}))
	if !model.selecting || model.autoFollow {
		t.Fatalf("left click must start direct selection and pause follow: selecting=%v follow=%v", model.selecting, model.autoFollow)
	}
	_, _ = model.Update(tea.MouseMotionMsg(tea.Mouse{Button: tea.MouseLeft, X: len(want) - 1, Y: model.viewport.YPosition}))
	got := model.selectedText()
	if !model.selectionSet || got != want {
		t.Fatalf("drag selection=%q, want %q", got, want)
	}
	if view := model.View(); view.MouseMode != tea.MouseModeCellMotion || !strings.Contains(view.Content, "\x1b[7m") {
		t.Fatalf("selection must stay in mouse mode and render highlight: mouse=%v content=%q", view.MouseMode, view.Content)
	}
	_, cmd := model.Update(tea.MouseReleaseMsg(tea.Mouse{Button: tea.MouseLeft, X: len(want) - 1, Y: model.viewport.YPosition}))
	if model.selecting || cmd != nil {
		t.Fatalf("release must only finish selection without copying: selecting=%v cmd=%v", model.selecting, cmd != nil)
	}
	if got := model.selectedText(); got != want {
		t.Fatalf("released selection=%q, want %q", got, want)
	}
	_, cmd = model.Update(tea.KeyPressMsg(tea.Key{Code: 'c', Mod: tea.ModCtrl}))
	if cmd == nil {
		t.Fatal("Ctrl+C with a selection must request clipboard copy")
	}
}

func TestWorkspaceTUISelectionStripsANSIAndSurvivesNewEvents(t *testing.T) {
	model := newWorkspaceTUIModel("demo", observation.NewTextRenderer(false), false)
	model.setSize(60, 10)
	model.lines = []string{"\x1b[31mred\x1b[0m text", "next"}
	model.viewport.SetContentLines(model.lines)
	model.snapToOffset(0, false)
	model.selectionAnchor = workspaceSelectionPoint{line: 0, column: 0}
	model.selectionFocus = workspaceSelectionPoint{line: 0, column: 7}
	model.selectionMoved = true
	model.selectionSet = true
	if got := model.selectedText(); got != "red text" {
		t.Fatalf("selected text=%q, want ANSI-stripped text", got)
	}
	before := model.viewport.YOffset()
	if _, err := model.consumeFrame(testWorkspaceTUIEvent(101, "selection.go")); err != nil {
		t.Fatal(err)
	}
	if model.viewport.YOffset() != before || model.autoFollow {
		t.Fatalf("new event must not yank a selected viewport: before=%d after=%d follow=%v", before, model.viewport.YOffset(), model.autoFollow)
	}
}

func TestWorkspaceTUIKeepsScrollPositionWhenNewEventsArrive(t *testing.T) {
	model := populatedWorkspaceTUIModel(t, 40)
	_, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyHome}))
	settleWorkspaceScroll(t, model)
	before := model.viewport.YOffset()
	if _, err := model.consumeFrame(testWorkspaceTUIEvent(102, "new.go")); err != nil {
		t.Fatal(err)
	}
	if model.viewport.YOffset() != before || model.autoFollow {
		t.Fatalf("new event must not yank a scrolled viewport to bottom: before=%d after=%d follow=%v", before, model.viewport.YOffset(), model.autoFollow)
	}
}

func populatedWorkspaceTUIModel(t *testing.T, count int) *workspaceTUIModel {
	t.Helper()
	model := newWorkspaceTUIModel("demo", observation.NewTextRenderer(false), false)
	model.setSize(60, 10)
	model.lines = make([]string, 0, count)
	for i := 1; i <= count; i++ {
		model.lines = append(model.lines, fmt.Sprintf("line-%02d", i))
	}
	model.viewport.SetContentLines(model.lines)
	model.snapToOffset(model.maxYOffset(), true)
	return model
}

func settleWorkspaceScroll(t *testing.T, model *workspaceTUIModel) {
	t.Helper()
	for frame := 0; frame < 240 && model.scrollAnimating; frame++ {
		model.stepScrollAnimation()
	}
	if model.scrollAnimating {
		t.Fatalf("scroll animation did not settle: position=%.3f velocity=%.3f target=%.3f", model.scrollPosition, model.scrollVelocity, model.scrollTarget)
	}
}

func testWorkspaceTUIEvent(sequence int64, path string) observation.Frame {
	return observation.Frame{
		Type: "event",
		Event: &observation.Event{
			Sequence:  sequence,
			RequestID: fmt.Sprintf("req_%d", sequence),
			Tool:      "file_read",
			Type:      observation.TypeToolCompleted,
			Input:     []byte(fmt.Sprintf(`{"path":%q}`, path)),
			Output:    []byte(`{"status":"ok"}`),
		},
	}
}
