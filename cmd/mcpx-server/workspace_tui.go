package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/x/ansi"

	"mcpx/internal/observation"
)

const (
	workspaceTUIFramesPerSecond = 60
	maxViewportBufferedLines    = 4096
	scrollSpringFrequency       = 14.0
	scrollSpringDampingRatio    = 1.0
	scrollRestDistance          = 0.04
	scrollRestVelocity          = 0.04
)

type workspaceFrameMsg struct {
	frame observation.Frame
}

type workspaceStreamDoneMsg struct {
	err error
}

type workspaceScrollTickMsg struct{}

type workspaceSelectionPoint struct {
	line   int
	column int
}

type workspaceTUIModel struct {
	workspace  string
	renderer   *observation.TextRenderer
	viewport   viewport.Model
	lines      []string
	status     string
	width      int
	height     int
	autoFollow bool
	color      bool
	streamErr  error

	selectionAnchor workspaceSelectionPoint
	selectionFocus  workspaceSelectionPoint
	selecting       bool
	selectionMoved  bool
	selectionSet    bool

	scrollSpring        harmonica.Spring
	scrollPosition      float64
	scrollVelocity      float64
	scrollTarget        float64
	scrollAnimating     bool
	scrollTickScheduled bool
}

func newWorkspaceTUIModel(workspace string, renderer *observation.TextRenderer, color bool) *workspaceTUIModel {
	width, height := terminalSize()
	if width <= 0 {
		width = 80
	}
	if height < 4 {
		height = defaultTerminalRows
	}
	vp := viewport.New(viewport.WithWidth(width), viewport.WithHeight(height-3))
	vp.KeyMap = viewport.DefaultKeyMap()
	vp.MouseWheelEnabled = true
	vp.MouseWheelDelta = 2
	vp.FillHeight = true
	vp.SoftWrap = false
	vp.YPosition = 2
	if renderer != nil {
		renderer.SetWidth(width)
	}
	return &workspaceTUIModel{
		workspace:      sanitizeDashboardLabel(workspace),
		renderer:       renderer,
		viewport:       vp,
		status:         "LIVE",
		width:          width,
		height:         height,
		autoFollow:     true,
		color:          color,
		scrollSpring:   harmonica.NewSpring(harmonica.FPS(workspaceTUIFramesPerSecond), scrollSpringFrequency, scrollSpringDampingRatio),
		scrollPosition: 0,
		scrollTarget:   0,
	}
}

func runWorkspaceTUI(ctx context.Context, client *observation.Client, request observation.SubscribeRequest, workspace string, renderer *observation.TextRenderer, color bool) error {
	if client == nil || renderer == nil {
		return fmt.Errorf("terminal UI requires observer client and text renderer")
	}
	streamCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	model := newWorkspaceTUIModel(workspace, renderer, color)
	program := tea.NewProgram(model, tea.WithFPS(workspaceTUIFramesPerSecond))
	go func() {
		err := client.Run(streamCtx, request, func(frame observation.Frame) error {
			program.Send(workspaceFrameMsg{frame: frame})
			return nil
		})
		program.Send(workspaceStreamDoneMsg{err: err})
	}()
	finalModel, runErr := program.Run()
	parentCanceled := ctx.Err() != nil
	cancel()
	if runErr != nil {
		return runErr
	}
	if final, ok := finalModel.(*workspaceTUIModel); ok && final.streamErr != nil && !parentCanceled && !errors.Is(final.streamErr, context.Canceled) {
		return final.streamErr
	}
	return nil
}

func (m *workspaceTUIModel) Init() tea.Cmd {
	return m.viewport.Init()
}

func (m *workspaceTUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.setSize(msg.Width, msg.Height)
		return m, nil
	case workspaceFrameMsg:
		cmd, err := m.consumeFrame(msg.frame)
		if err != nil {
			m.status = "ERROR"
			m.streamErr = err
			return m, tea.Quit
		}
		return m, cmd
	case workspaceStreamDoneMsg:
		if msg.err != nil && !errors.Is(msg.err, context.Canceled) {
			m.status = "ERROR"
			m.streamErr = msg.err
		}
		return m, tea.Quit
	case workspaceScrollTickMsg:
		return m, m.stepScrollAnimation()
	case tea.KeyPressMsg:
		return m.updateKey(msg)
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			m.beginSelection(msg.Mouse())
		}
		return m, nil
	case tea.MouseMotionMsg:
		if m.selecting && msg.Button == tea.MouseLeft {
			m.updateSelection(msg.Mouse())
		}
		return m, nil
	case tea.MouseReleaseMsg:
		if m.selecting {
			return m, m.finishSelection(msg.Mouse())
		}
		return m, nil
	case tea.MouseWheelMsg:
		switch msg.Button {
		case tea.MouseWheelUp:
			return m, m.scrollBy(-m.viewport.MouseWheelDelta)
		case tea.MouseWheelDown:
			return m, m.scrollBy(m.viewport.MouseWheelDelta)
		default:
			updated, cmd := m.viewport.Update(msg)
			m.viewport = updated
			return m, cmd
		}
	default:
		updated, cmd := m.viewport.Update(msg)
		m.viewport = updated
		return m, cmd
	}
}

func (m *workspaceTUIModel) updateKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if key == "ctrl+c" {
		if cmd := m.copySelection(); cmd != nil {
			return m, cmd
		}
		return m, tea.Quit
	}
	if key == "q" {
		return m, tea.Quit
	}
	switch msg.Key().Code {
	case tea.KeyHome, tea.KeyKpHome:
		return m, m.animateTo(0, false)
	case tea.KeyEnd, tea.KeyKpEnd:
		return m, m.animateTo(float64(m.maxYOffset()), true)
	}

	page := m.viewport.Height()
	if page < 1 {
		page = 1
	}
	halfPage := page / 2
	if halfPage < 1 {
		halfPage = 1
	}
	switch key {
	case "g":
		return m, m.animateTo(0, false)
	case "G":
		return m, m.animateTo(float64(m.maxYOffset()), true)
	case "up", "k", "ctrl+p":
		return m, m.scrollBy(-1)
	case "down", "j", "ctrl+n":
		return m, m.scrollBy(1)
	case "pgup", "b", "ctrl+b":
		return m, m.scrollBy(-page)
	case "pgdown", "f", "ctrl+f", " ":
		return m, m.scrollBy(page)
	case "u", "ctrl+u":
		return m, m.scrollBy(-halfPage)
	case "d", "ctrl+d":
		return m, m.scrollBy(halfPage)
	}

	updated, cmd := m.viewport.Update(msg)
	m.viewport = updated
	return m, cmd
}

func (m *workspaceTUIModel) beginSelection(mouse tea.Mouse) {
	point, ok := m.mouseSelectionPoint(mouse, false)
	if !ok {
		return
	}
	m.autoFollow = false
	m.scrollAnimating = false
	m.scrollVelocity = 0
	m.scrollPosition = float64(m.viewport.YOffset())
	m.scrollTarget = m.scrollPosition
	m.selectionAnchor = point
	m.selectionFocus = point
	m.selecting = true
	m.selectionMoved = false
	m.selectionSet = false
}

func (m *workspaceTUIModel) updateSelection(mouse tea.Mouse) {
	if !m.selecting {
		return
	}
	point, ok := m.mouseSelectionPoint(mouse, true)
	if !ok {
		return
	}
	m.selectionFocus = point
	if point != m.selectionAnchor {
		m.selectionMoved = true
		m.selectionSet = true
	}
}

func (m *workspaceTUIModel) finishSelection(mouse tea.Mouse) tea.Cmd {
	if !m.selecting {
		return nil
	}
	m.updateSelection(mouse)
	m.selecting = false
	if !m.selectionMoved || m.selectedText() == "" {
		m.clearSelection()
	}
	return nil
}

func (m *workspaceTUIModel) copySelection() tea.Cmd {
	selected := m.selectedText()
	if selected == "" {
		return nil
	}
	return tea.SetClipboard(selected)
}

func (m *workspaceTUIModel) mouseSelectionPoint(mouse tea.Mouse, clampToViewport bool) (workspaceSelectionPoint, bool) {
	if len(m.lines) == 0 || m.viewport.Height() <= 0 {
		return workspaceSelectionPoint{}, false
	}
	bodyY := mouse.Y - m.viewport.YPosition
	if clampToViewport {
		if bodyY < 0 {
			bodyY = 0
		}
		if bodyY >= m.viewport.Height() {
			bodyY = m.viewport.Height() - 1
		}
	} else if bodyY < 0 || bodyY >= m.viewport.Height() {
		return workspaceSelectionPoint{}, false
	}
	line := m.viewport.YOffset() + bodyY
	if line < 0 {
		line = 0
	}
	if line >= len(m.lines) {
		line = len(m.lines) - 1
	}
	column := m.viewport.XOffset() + mouse.X
	if column < 0 {
		column = 0
	}
	lineWidth := m.lineWidth(line)
	if column > lineWidth {
		column = lineWidth
	}
	return workspaceSelectionPoint{line: line, column: column}, true
}

func (m *workspaceTUIModel) lineWidth(line int) int {
	if line < 0 || line >= len(m.lines) {
		return 0
	}
	return ansi.StringWidth(m.lines[line])
}

func selectionPointLess(a, b workspaceSelectionPoint) bool {
	return a.line < b.line || (a.line == b.line && a.column < b.column)
}

func (m *workspaceTUIModel) selectionBounds() (workspaceSelectionPoint, workspaceSelectionPoint, bool) {
	if !m.selectionSet || !m.selectionMoved {
		return workspaceSelectionPoint{}, workspaceSelectionPoint{}, false
	}
	start := m.selectionAnchor
	endCell := m.selectionFocus
	if selectionPointLess(endCell, start) {
		start, endCell = endCell, start
	}
	end := endCell
	lineWidth := m.lineWidth(end.line)
	if end.column < lineWidth {
		end.column++
	}
	if start == end {
		return workspaceSelectionPoint{}, workspaceSelectionPoint{}, false
	}
	return start, end, true
}

func (m *workspaceTUIModel) selectedText() string {
	start, end, ok := m.selectionBounds()
	if !ok {
		return ""
	}
	parts := make([]string, 0, end.line-start.line+1)
	for line := start.line; line <= end.line && line < len(m.lines); line++ {
		plain := ansi.Strip(m.lines[line])
		width := ansi.StringWidth(plain)
		from, to := 0, width
		if line == start.line {
			from = min(start.column, width)
		}
		if line == end.line {
			to = min(end.column, width)
		}
		if to < from {
			to = from
		}
		parts = append(parts, ansi.Cut(plain, from, to))
	}
	return strings.Join(parts, "\n")
}

func (m *workspaceTUIModel) clearSelection() {
	m.selecting = false
	m.selectionMoved = false
	m.selectionSet = false
}

func (m *workspaceTUIModel) adjustSelectionForDroppedLines(dropped int) {
	if dropped <= 0 || (!m.selectionSet && !m.selecting) {
		return
	}
	if m.selectionAnchor.line < dropped || m.selectionFocus.line < dropped {
		m.clearSelection()
		return
	}
	m.selectionAnchor.line -= dropped
	m.selectionFocus.line -= dropped
}

func (m *workspaceTUIModel) renderSelection(body string) string {
	start, end, ok := m.selectionBounds()
	if !ok {
		return body
	}
	visible := strings.Split(body, "\n")
	xOffset := m.viewport.XOffset()
	style := lipgloss.NewStyle().Reverse(true)
	for index := range visible {
		line := m.viewport.YOffset() + index
		if line < start.line || line > end.line || line >= len(m.lines) {
			continue
		}
		from, to := 0, m.lineWidth(line)
		if line == start.line {
			from = start.column
		}
		if line == end.line {
			to = end.column
		}
		from -= xOffset
		to -= xOffset
		if from < 0 {
			from = 0
		}
		if to > m.viewport.Width() {
			to = m.viewport.Width()
		}
		if to <= from {
			continue
		}
		visible[index] = lipgloss.StyleRanges(visible[index], lipgloss.NewRange(from, to, style))
	}
	return strings.Join(visible, "\n")
}

func (m *workspaceTUIModel) scrollBy(delta int) tea.Cmd {
	base := m.scrollTarget
	if !m.scrollAnimating {
		base = float64(m.viewport.YOffset())
	}
	target := m.clampScrollOffset(base + float64(delta))
	follow := int(math.Round(target)) >= m.maxYOffset()
	return m.animateTo(target, follow)
}

func (m *workspaceTUIModel) animateTo(target float64, follow bool) tea.Cmd {
	target = m.clampScrollOffset(target)
	m.scrollTarget = target
	m.autoFollow = follow
	if math.Abs(m.scrollTarget-m.scrollPosition) <= scrollRestDistance && math.Abs(m.scrollVelocity) <= scrollRestVelocity {
		m.snapToOffset(int(math.Round(m.scrollTarget)), follow)
		return nil
	}
	m.scrollAnimating = true
	return m.ensureScrollTick()
}

func (m *workspaceTUIModel) ensureScrollTick() tea.Cmd {
	if m.scrollTickScheduled || !m.scrollAnimating {
		return nil
	}
	m.scrollTickScheduled = true
	return tea.Tick(time.Second/time.Duration(workspaceTUIFramesPerSecond), func(time.Time) tea.Msg {
		return workspaceScrollTickMsg{}
	})
}

func (m *workspaceTUIModel) stepScrollAnimation() tea.Cmd {
	m.scrollTickScheduled = false
	if !m.scrollAnimating {
		return nil
	}
	m.scrollTarget = m.clampScrollOffset(m.scrollTarget)
	m.scrollPosition, m.scrollVelocity = m.scrollSpring.Update(m.scrollPosition, m.scrollVelocity, m.scrollTarget)
	m.scrollPosition = m.clampScrollOffset(m.scrollPosition)
	if math.Abs(m.scrollTarget-m.scrollPosition) <= scrollRestDistance && math.Abs(m.scrollVelocity) <= scrollRestVelocity {
		m.snapToOffset(int(math.Round(m.scrollTarget)), m.autoFollow)
		return nil
	}
	m.viewport.SetYOffset(int(math.Round(m.scrollPosition)))
	return m.ensureScrollTick()
}

func (m *workspaceTUIModel) snapToOffset(offset int, follow bool) {
	maxOffset := m.maxYOffset()
	if offset < 0 {
		offset = 0
	}
	if offset > maxOffset {
		offset = maxOffset
	}
	m.viewport.SetYOffset(offset)
	m.scrollPosition = float64(offset)
	m.scrollTarget = float64(offset)
	m.scrollVelocity = 0
	m.scrollAnimating = false
	m.autoFollow = follow
}

func (m *workspaceTUIModel) clampScrollOffset(offset float64) float64 {
	if offset < 0 {
		return 0
	}
	maxOffset := float64(m.maxYOffset())
	if offset > maxOffset {
		return maxOffset
	}
	return offset
}

func (m *workspaceTUIModel) maxYOffset() int {
	maxOffset := m.viewport.TotalLineCount() - m.viewport.Height()
	if maxOffset < 0 {
		return 0
	}
	return maxOffset
}

func (m *workspaceTUIModel) setSize(width, height int) {
	if width <= 0 {
		width = 80
	}
	if height < 4 {
		height = 4
	}
	wasFollowing := m.autoFollow
	m.width = width
	m.height = height
	m.viewport.SetWidth(width)
	m.viewport.SetHeight(height - 3)
	m.viewport.YPosition = 2
	if m.renderer != nil {
		m.renderer.SetWidth(width)
	}
	if wasFollowing {
		m.snapToOffset(m.maxYOffset(), true)
		return
	}
	m.scrollPosition = m.clampScrollOffset(m.scrollPosition)
	m.scrollTarget = m.clampScrollOffset(m.scrollTarget)
	m.viewport.SetYOffset(int(math.Round(m.scrollPosition)))
}

func (m *workspaceTUIModel) consumeFrame(frame observation.Frame) (tea.Cmd, error) {
	if m.renderer == nil {
		return nil, fmt.Errorf("terminal UI text renderer is required")
	}
	width := m.width
	if width <= 0 {
		width = 80
	}
	var delta bytes.Buffer
	if err := renderWorkspaceFrameWithRendererAtWidth(&delta, frame, "text", m.color, m.renderer, width); err != nil {
		return nil, err
	}
	switch frame.Type {
	case "gap":
		m.status = "RECONNECTED"
	case "error":
		m.status = "ERROR"
	default:
		m.status = "LIVE"
	}
	newLines := outputLines(delta.String())
	if len(newLines) == 0 {
		return nil, nil
	}
	wasFollowing := m.autoFollow
	dropped := m.appendLines(newLines)
	m.adjustSelectionForDroppedLines(dropped)
	m.viewport.SetContentLines(m.lines)
	if wasFollowing {
		return m.animateTo(float64(m.maxYOffset()), true), nil
	}
	if dropped > 0 {
		m.scrollPosition -= float64(dropped)
		m.scrollTarget -= float64(dropped)
		m.scrollPosition = m.clampScrollOffset(m.scrollPosition)
		m.scrollTarget = m.clampScrollOffset(m.scrollTarget)
		m.viewport.SetYOffset(int(math.Round(m.scrollPosition)))
	}
	return nil, nil
}

func (m *workspaceTUIModel) appendLines(lines []string) int {
	if len(lines) == 0 {
		return 0
	}
	m.lines = append(m.lines, lines...)
	if len(m.lines) <= maxViewportBufferedLines {
		return 0
	}
	dropped := len(m.lines) - maxViewportBufferedLines
	m.lines = append([]string(nil), m.lines[dropped:]...)
	return dropped
}

func outputLines(output string) []string {
	output = strings.TrimSuffix(output, "\n")
	if output == "" {
		return nil
	}
	return strings.Split(output, "\n")
}

func (m *workspaceTUIModel) View() tea.View {
	width := m.width
	if width <= 0 {
		width = 80
	}
	workspace := m.workspace
	if workspace == "" {
		workspace = "workspace"
	}
	header := truncateTerminalLine("MCPX ARC · "+workspace, width)
	separator := strings.Repeat("─", width)
	total := m.viewport.TotalLineCount()
	start, end := 0, 0
	if total > 0 {
		start = m.viewport.YOffset() + 1
		end = m.viewport.YOffset() + m.viewport.VisibleLineCount()
		if end > total {
			end = total
		}
	}
	status := strings.TrimSpace(m.status)
	if status == "" {
		status = "LIVE"
	}
	mode := "SCROLL"
	if m.autoFollow {
		mode = "FOLLOW"
	}
	footer := fmt.Sprintf("%s · %s · %d-%d/%d · select · Ctrl+C copy · wheel · ↑↓ PgUp/PgDn Home/End", status, mode, start, end, total)
	body := m.renderSelection(m.viewport.View())
	view := tea.NewView(header + "\n" + separator + "\n" + body + "\n" + truncateTerminalLine(footer, width))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
	view.DisableBracketedPasteMode = true
	view.WindowTitle = "MCPX ARC · " + workspace
	return view
}

func sanitizeDashboardLabel(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(value))
}

func truncateTerminalLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return string(runes[:1])
	}
	return string(runes[:width-1]) + "…"
}
