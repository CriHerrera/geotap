package views

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	_ "modernc.org/sqlite"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/rendis/geotap/internal/model"
	"github.com/rendis/geotap/internal/tui/styles"
)

type focusArea int

const (
	focusTable focusArea = iota
	focusFilter
	focusCard
	focusJSON
)

// ExplorerModel displays scraped data with table + detail panels.
type ExplorerModel struct {
	dbPath     string
	businesses []model.Business
	filtered   []model.Business
	table      table.Model
	filter     textinput.Model
	focus      focusArea
	selected   int
	width      int
	height     int
	err        error
	total      int
	exportMsg  string

	// Scroll state for detail panels
	cardScrollY int
	cardLines   []string // cached rendered card lines
	jsonScrollY int
	jsonScrollX int
	jsonLines   []string // cached raw JSON lines
	jsonRaw     string   // full JSON for clipboard copy
}

type dbLoadedMsg struct {
	Businesses []model.Business
	Err        error
}

func NewExplorerModel(dbPath string) ExplorerModel {
	filter := textinput.New()
	filter.Placeholder = "Type to filter..."
	filter.CharLimit = 50

	return ExplorerModel{
		dbPath:   dbPath,
		filter:   filter,
		selected: -1,
	}
}

func (m ExplorerModel) Init() tea.Cmd {
	return func() tea.Msg {
		businesses, err := loadBusinesses(m.dbPath)
		return dbLoadedMsg{Businesses: businesses, Err: err}
	}
}

func (m ExplorerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.updateLayout()
	case tea.KeyMsg:
		key := msg.String()

		// Global keys
		if key == "ctrl+c" {
			return m, tea.Quit
		}

		switch m.focus {
		case focusTable:
			switch key {
			case "esc", "q":
				return m, func() tea.Msg { return NavigateToHome{} }
			case "/":
				m.focus = focusFilter
				m.filter.Focus()
				return m, textinput.Blink
			case "tab":
				m.focus = focusFilter
				m.filter.Focus()
				return m, textinput.Blink
			case "1":
				m.focus = focusCard
				m.table.SetStyles(m.unfocusedTableStyles())
				return m, nil
			case "2":
				m.focus = focusJSON
				m.table.SetStyles(m.unfocusedTableStyles())
				return m, nil
			case "e":
				m.exportCSV()
				return m, nil
			}

		case focusFilter:
			switch key {
			case "esc", "enter", "tab":
				m.focus = focusTable
				m.filter.Blur()
				return m, nil
			}

		case focusCard:
			ph := m.panelHeight()
			maxScroll := len(m.cardLines) - ph
			if maxScroll < 0 {
				maxScroll = 0
			}
			switch key {
			case "esc":
				m.focus = focusTable
				m.table.SetStyles(m.focusedTableStyles())
				return m, nil
			case "up", "k":
				if m.cardScrollY > 0 {
					m.cardScrollY--
				}
				return m, nil
			case "down", "j":
				if m.cardScrollY < maxScroll {
					m.cardScrollY++
				}
				return m, nil
			}

		case focusJSON:
			ph := m.panelHeight()
			maxScroll := len(m.jsonLines) - ph
			if maxScroll < 0 {
				maxScroll = 0
			}
			switch key {
			case "esc":
				m.focus = focusTable
				m.table.SetStyles(m.focusedTableStyles())
				return m, nil
			case "up", "k":
				if m.jsonScrollY > 0 {
					m.jsonScrollY--
				}
				return m, nil
			case "down", "j":
				if m.jsonScrollY < maxScroll {
					m.jsonScrollY++
				}
				return m, nil
			case "left", "h":
				if m.jsonScrollX > 0 {
					m.jsonScrollX -= 4
					if m.jsonScrollX < 0 {
						m.jsonScrollX = 0
					}
				}
				return m, nil
			case "right", "l":
				m.jsonScrollX += 4
				return m, nil
			case "c":
				m.copyToClipboard()
				return m, nil
			}
		}

	case dbLoadedMsg:
		if msg.Err != nil {
			m.err = msg.Err
			return m, nil
		}
		m.businesses = msg.Businesses
		m.filtered = msg.Businesses
		m.total = len(m.businesses)
		m.buildTable(m.businesses)
		m.updateLayout()
		if len(m.filtered) > 0 {
			m.selected = 0
			m.cacheDetailContent()
		}
		return m, nil
	}

	// Route input to focused area
	var cmd tea.Cmd
	switch m.focus {
	case focusTable:
		m.table, cmd = m.table.Update(msg)
		cursor := m.table.Cursor()
		if cursor != m.selected && cursor < len(m.filtered) {
			m.selected = cursor
			m.cardScrollY = 0
			m.jsonScrollY = 0
			m.jsonScrollX = 0
			m.cacheDetailContent()
		}
	case focusFilter:
		m.filter, cmd = m.filter.Update(msg)
		m.applyFilter()
	}

	return m, cmd
}

func (m *ExplorerModel) cacheDetailContent() {
	if m.selected < 0 || m.selected >= len(m.filtered) {
		m.cardLines = nil
		m.jsonLines = nil
		m.jsonRaw = ""
		return
	}

	// Cache card content as plain lines
	biz := m.filtered[m.selected]
	m.cardLines = m.buildCardLines(biz)

	// Cache JSON
	data, err := json.MarshalIndent(biz, "", "  ")
	if err != nil {
		m.jsonLines = []string{"JSON error"}
		m.jsonRaw = ""
		return
	}
	m.jsonRaw = string(data)
	m.jsonLines = strings.Split(m.jsonRaw, "\n")
}

func (m ExplorerModel) buildCardLines(biz model.Business) []string {
	var lines []string

	lines = append(lines, biz.Name)

	if biz.Rating > 0 {
		r := fmt.Sprintf("%.1f", biz.Rating)
		if biz.ReviewCount > 0 {
			r += fmt.Sprintf(" (%d reviews)", biz.ReviewCount)
		}
		lines = append(lines, r)
	}

	if biz.Categories != "" {
		lines = append(lines, biz.Categories)
	}

	lines = append(lines, "")

	addRow := func(label, value string) {
		if value != "" {
			lines = append(lines, fmt.Sprintf("%-10s %s", label, value))
		}
	}

	addRow("Address:", biz.Address)
	if biz.City != "" {
		parts := []string{biz.City}
		if biz.PostalCode != "" {
			parts = append(parts, biz.PostalCode)
		}
		if biz.CountryCode != "" {
			parts = append(parts, biz.CountryCode)
		}
		addRow("City:", strings.Join(parts, ", "))
	}
	addRow("Phone:", biz.Phone)
	addRow("Website:", biz.Website)
	addRow("Maps:", biz.GoogleURL)
	addRow("Price:", biz.PriceRange)
	if biz.Lat != 0 || biz.Lng != 0 {
		addRow("Coords:", fmt.Sprintf("%.6f, %.6f", biz.Lat, biz.Lng))
	}
	addRow("CID:", biz.CID)
	addRow("PlaceID:", biz.PlaceID)

	if biz.OpenHours != "" {
		lines = append(lines, "")
		addRow("Hours:", biz.OpenHours)
	}

	if biz.Description != "" {
		lines = append(lines, "")
		lines = append(lines, biz.Description)
	}

	return lines
}

func (m *ExplorerModel) buildTable(businesses []model.Business) {
	nameW := 28
	catW := 20
	cityW := 14
	ratingW := 6
	phoneW := 16
	if m.width > 120 {
		extra := m.width - 120
		nameW += extra * 3 / 10
		catW += extra * 3 / 10
		cityW += extra * 2 / 10
		phoneW += extra * 2 / 10
	}

	columns := []table.Column{
		{Title: "Name", Width: nameW},
		{Title: "Category", Width: catW},
		{Title: "City", Width: cityW},
		{Title: "Rating", Width: ratingW},
		{Title: "Phone", Width: phoneW},
	}

	rows := make([]table.Row, len(businesses))
	for i, b := range businesses {
		rating := ""
		if b.Rating > 0 {
			rating = fmt.Sprintf("%.1f", b.Rating)
		}
		rows[i] = table.Row{
			truncate(b.Name, nameW),
			truncate(b.Category, catW),
			truncate(b.City, cityW),
			rating,
			b.Phone,
		}
	}

	t := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(10),
	)
	t.SetStyles(m.focusedTableStyles())
	m.table = t
}

func (m ExplorerModel) focusedTableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(styles.Muted).
		BorderBottom(true).
		Bold(true).
		Foreground(styles.Secondary)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("#FFFFFF")).
		Background(styles.Primary).
		Bold(true)
	return s
}

func (m ExplorerModel) unfocusedTableStyles() table.Styles {
	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(styles.Muted).
		BorderBottom(true).
		Bold(true).
		Foreground(styles.Muted)
	s.Selected = s.Selected.
		Foreground(styles.Text).
		Background(lipgloss.Color("#333333")).
		Bold(false)
	return s
}

func (m ExplorerModel) panelHeight() int {
	h := m.height/2 - 6
	if h < 6 {
		h = 6
	}
	return h
}

func (m *ExplorerModel) updateLayout() {
	if m.width <= 0 {
		return
	}
	tableH := m.height/2 - 4
	if tableH < 5 {
		tableH = 5
	}
	m.table.SetHeight(tableH)
	m.buildTable(m.filtered)
}

// normalize removes accents/diacritics and lowercases text for fuzzy matching.
func normalize(s string) string {
	t := transform.Chain(norm.NFD, transform.RemoveFunc(func(r rune) bool {
		return unicode.Is(unicode.Mn, r)
	}), norm.NFC)
	result, _, _ := transform.String(t, strings.ToLower(s))
	return result
}

func (m *ExplorerModel) applyFilter() {
	raw := strings.TrimSpace(m.filter.Value())
	if raw == "" {
		m.filtered = m.businesses
		m.buildTable(m.filtered)
		if len(m.filtered) > 0 {
			m.selected = 0
			m.cacheDetailContent()
		}
		return
	}

	words := strings.Fields(normalize(raw))
	m.filtered = nil
	for _, b := range m.businesses {
		haystack := normalize(strings.Join([]string{
			b.Name, b.Category, b.Categories, b.City,
			b.Address, b.Description,
		}, " "))
		match := true
		for _, w := range words {
			if !strings.Contains(haystack, w) {
				match = false
				break
			}
		}
		if match {
			m.filtered = append(m.filtered, b)
		}
	}
	m.buildTable(m.filtered)
	if len(m.filtered) > 0 {
		m.selected = 0
	} else {
		m.selected = -1
	}
	m.cacheDetailContent()
}

func (m ExplorerModel) View() string {
	if m.err != nil {
		return styles.ErrorText.Render(fmt.Sprintf("Error loading DB: %v", m.err))
	}

	var b strings.Builder

	b.WriteString(styles.Title.Render(fmt.Sprintf("Explorer: %d businesses", m.total)))
	if len(m.filtered) != m.total {
		b.WriteString(lipgloss.NewStyle().Foreground(styles.Muted).
			Render(fmt.Sprintf(" (showing %d)", len(m.filtered))))
	}
	b.WriteString("\n\n")

	// Filter
	filterStyle := lipgloss.NewStyle().Foreground(styles.Muted)
	if m.focus == focusFilter {
		filterStyle = lipgloss.NewStyle().Foreground(styles.Primary)
	}
	b.WriteString(filterStyle.Render("Filter: "))
	b.WriteString(m.filter.View())
	b.WriteString("\n")

	// Table
	b.WriteString(m.table.View())
	b.WriteString("\n\n")

	// Detail panels
	detailW := m.width - 2
	if detailW < 40 {
		detailW = 40
	}

	// Panel height for viewports
	panelH := m.height/2 - 6
	if panelH < 6 {
		panelH = 6
	}

	cardOuterW := detailW * 2 / 5
	jsonOuterW := detailW - cardOuterW - 1

	// Card panel
	cardBorderColor := styles.Muted
	if m.focus == focusCard {
		cardBorderColor = styles.Primary
	}
	cardInnerW := cardOuterW - 4
	if cardInnerW < 20 {
		cardInnerW = 20
	}
	cardContent := m.viewCardPanel(cardInnerW, panelH)
	cardBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(cardBorderColor).
		Padding(0, 1).
		Width(cardOuterW - 2).
		Height(panelH).
		Render(cardContent)
	cardLabel := lipgloss.NewStyle().Bold(true).Foreground(cardBorderColor).Render("[1] Details")
	cardBox = cardLabel + "\n" + cardBox

	// JSON panel
	jsonBorderColor := styles.Muted
	if m.focus == focusJSON {
		jsonBorderColor = styles.Primary
	}
	jsonInnerW := jsonOuterW - 4
	if jsonInnerW < 20 {
		jsonInnerW = 20
	}
	jsonContent := m.viewJSONPanel(jsonInnerW, panelH)
	jsonBox := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(jsonBorderColor).
		Padding(0, 1).
		Width(jsonOuterW - 2).
		Height(panelH).
		Render(jsonContent)
	jsonLabel := lipgloss.NewStyle().Bold(true).Foreground(jsonBorderColor).Render("[2] JSON")
	jsonBox = jsonLabel + "\n" + jsonBox

	b.WriteString(lipgloss.JoinHorizontal(lipgloss.Top, cardBox, " ", jsonBox))
	b.WriteString("\n\n")

	// Export message
	if m.exportMsg != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(styles.Success).Render(m.exportMsg))
		b.WriteString("\n")
	}

	// Status bar changes by focus
	var statusText string
	switch m.focus {
	case focusTable:
		statusText = "↑↓ navigate • 1 details • 2 json • / filter • e export • esc back"
	case focusFilter:
		statusText = "type to filter • esc back"
	case focusCard:
		statusText = "↑↓ scroll • esc back to table"
	case focusJSON:
		statusText = "↑↓ scroll • ←→ pan • c copy json • esc back to table"
	}
	b.WriteString(styles.StatusBar.Render(statusText))

	return b.String()
}

func (m ExplorerModel) viewCardPanel(w, h int) string {
	if m.selected < 0 || m.selected >= len(m.filtered) || len(m.cardLines) == 0 {
		return lipgloss.NewStyle().Foreground(styles.Muted).Italic(true).
			Render("Select a business\nto view details")
	}

	lines := m.cardLines

	// Clamp scroll
	scrollY := m.cardScrollY
	if scrollY > len(lines)-h {
		scrollY = len(lines) - h
	}
	if scrollY < 0 {
		scrollY = 0
	}

	// Window
	end := scrollY + h
	if end > len(lines) {
		end = len(lines)
	}
	visible := lines[scrollY:end]

	var sb strings.Builder
	label := lipgloss.NewStyle().Foreground(styles.Muted)
	valStyle := lipgloss.NewStyle().Foreground(styles.Text)

	for i, line := range visible {
		// First line (name) is bold
		if scrollY+i == 0 {
			sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(styles.Text).
				Render(truncate(line, w)))
		} else if scrollY+i == 1 && strings.Contains(line, "review") {
			// Rating line
			sb.WriteString(lipgloss.NewStyle().Foreground(styles.Warning).
				Render(truncate(line, w)))
		} else if strings.HasPrefix(line, "Website:") || strings.HasPrefix(line, "Maps:") {
			parts := strings.SplitN(line, " ", 2)
			lbl := parts[0]
			val := ""
			if len(parts) > 1 {
				val = strings.TrimSpace(parts[1])
			}
			sb.WriteString(label.Render(fmt.Sprintf("%-10s ", lbl)))
			sb.WriteString(lipgloss.NewStyle().Foreground(styles.Primary).
				Render(truncate(val, w-11)))
		} else {
			sb.WriteString(valStyle.Render(truncate(line, w)))
		}
		if i < len(visible)-1 {
			sb.WriteString("\n")
		}
	}

	// Scroll indicators
	if scrollY > 0 {
		sb.WriteString("\n")
		sb.WriteString(label.Render("  ▲ more above"))
	}
	if end < len(lines) {
		sb.WriteString("\n")
		sb.WriteString(label.Render("  ▼ more below"))
	}

	return sb.String()
}

func (m ExplorerModel) viewJSONPanel(w, h int) string {
	if m.selected < 0 || m.selected >= len(m.filtered) || len(m.jsonLines) == 0 {
		return lipgloss.NewStyle().Foreground(styles.Muted).Italic(true).
			Render("Select a business\nto view JSON")
	}

	lines := m.jsonLines
	jsonStyle := lipgloss.NewStyle().Foreground(styles.Muted)
	keyStyle := lipgloss.NewStyle().Foreground(styles.Secondary)
	strStyle := lipgloss.NewStyle().Foreground(styles.Success)

	// Clamp scroll
	scrollY := m.jsonScrollY
	if scrollY > len(lines)-h {
		scrollY = len(lines) - h
	}
	if scrollY < 0 {
		scrollY = 0
	}

	end := scrollY + h
	if end > len(lines) {
		end = len(lines)
	}
	visible := lines[scrollY:end]

	var sb strings.Builder
	for i, line := range visible {
		// Apply horizontal scroll
		display := line
		if m.jsonScrollX > 0 {
			if m.jsonScrollX < len(display) {
				display = display[m.jsonScrollX:]
			} else {
				display = ""
			}
		}
		if len(display) > w {
			display = display[:w-1] + "…"
		}

		// Simple JSON syntax coloring
		trimmed := strings.TrimSpace(display)
		if strings.HasPrefix(trimmed, "\"") && strings.Contains(trimmed, "\":") {
			// Key line: color the key part
			colonIdx := strings.Index(display, "\":")
			if colonIdx > 0 {
				keyPart := display[:colonIdx+1]
				valPart := display[colonIdx+1:]
				sb.WriteString(keyStyle.Render(keyPart))
				sb.WriteString(strStyle.Render(valPart))
			} else {
				sb.WriteString(jsonStyle.Render(display))
			}
		} else {
			sb.WriteString(jsonStyle.Render(display))
		}

		if i < len(visible)-1 {
			sb.WriteString("\n")
		}
	}

	// Scroll indicators
	if scrollY > 0 || end < len(lines) {
		sb.WriteString("\n")
		indicator := fmt.Sprintf("  [%d/%d]", scrollY+1, len(lines))
		if m.jsonScrollX > 0 {
			indicator += fmt.Sprintf(" ←%d", m.jsonScrollX)
		}
		sb.WriteString(lipgloss.NewStyle().Foreground(styles.Muted).Render(indicator))
	}

	return sb.String()
}

func (m *ExplorerModel) copyToClipboard() {
	if m.jsonRaw == "" {
		return
	}
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(m.jsonRaw)
	if err := cmd.Run(); err != nil {
		m.exportMsg = fmt.Sprintf("Copy failed: %v", err)
		return
	}
	m.exportMsg = "JSON copied to clipboard"
}

func truncate(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func (m *ExplorerModel) exportCSV() {
	dir := filepath.Dir(m.dbPath)
	base := strings.TrimSuffix(filepath.Base(m.dbPath), ".db")
	csvPath := filepath.Join(dir, base+".csv")

	f, err := os.Create(csvPath)
	if err != nil {
		m.exportMsg = fmt.Sprintf("Export error: %v", err)
		return
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	w.Write([]string{
		"name", "rating", "review_count", "category", "categories",
		"address", "city", "postal_code", "country_code",
		"lat", "lng", "phone", "website", "google_url",
		"description", "price_range", "query",
	})

	data := m.filtered
	if len(data) == 0 {
		data = m.businesses
	}

	for _, b := range data {
		w.Write([]string{
			b.Name,
			fmt.Sprintf("%.1f", b.Rating),
			fmt.Sprintf("%d", b.ReviewCount),
			b.Category,
			b.Categories,
			b.Address,
			b.City,
			b.PostalCode,
			b.CountryCode,
			fmt.Sprintf("%.6f", b.Lat),
			fmt.Sprintf("%.6f", b.Lng),
			b.Phone,
			b.Website,
			b.GoogleURL,
			b.Description,
			b.PriceRange,
			b.Query,
		})
	}

	m.exportMsg = fmt.Sprintf("Exported %d rows to %s", len(data), csvPath)
}

func loadBusinesses(dbPath string) ([]model.Business, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`
		SELECT name, rating, review_count, category, address, price_range,
		       lat, lng, cid, phone, website, google_url, description, place_id,
		       open_hours, thumbnail, COALESCE(photos, '') as photos,
		       categories, city, postal_code, country_code, query
		FROM businesses ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var businesses []model.Business
	for rows.Next() {
		var b model.Business
		err := rows.Scan(
			&b.Name, &b.Rating, &b.ReviewCount, &b.Category, &b.Address, &b.PriceRange,
			&b.Lat, &b.Lng, &b.CID, &b.Phone, &b.Website, &b.GoogleURL, &b.Description, &b.PlaceID,
			&b.OpenHours, &b.Thumbnail, &b.Photos, &b.Categories, &b.City, &b.PostalCode, &b.CountryCode, &b.Query,
		)
		if err != nil {
			continue
		}
		businesses = append(businesses, b)
	}
	return businesses, nil
}
