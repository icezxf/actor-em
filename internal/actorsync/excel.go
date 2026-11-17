package actorsync

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/xuri/excelize/v2"
)

// 导出的列顺序，和下面的取值一一对应。
var xlsxHeaders = []string{
	"名字", "原名", "别名", "状态", "生日", "出生地", "身高cm", "体重kg",
	"罩杯", "腰围cm", "臀围cm", "出道年", "简介", "头像直链", "头像路径",
	"来源", "外部ID", "最近抓取", "失败次数", "Key",
}

var xlsxWidths = map[string]float64{
	"A": 18, "B": 18, "C": 24, "D": 10, "E": 12, "F": 10,
	"G": 8, "H": 8, "I": 6, "J": 8, "K": 8, "L": 8,
	"M": 60, "N": 34, "O": 30, "P": 22, "Q": 40, "R": 20, "S": 8, "T": 26,
}

// ExportXLSX 把演员库导成 Excel，方便手工查询/筛选。
// 只读派生产物：改 Excel 不会回流，真源仍是 actors.json。
func ExportXLSX(path string, db *Db) error {
	f := excelize.NewFile()
	const sheet = "演员库"

	idx, err := f.NewSheet(sheet)
	if err != nil {
		return err
	}
	f.SetActiveSheet(idx)
	if err := f.DeleteSheet("Sheet1"); err != nil {
		return err
	}

	headerStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Bold: true, Color: "FFFFFF"},
		Fill:      excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{"2F5597"}},
		Alignment: &excelize.Alignment{Horizontal: "center", Vertical: "center"},
	})
	linkStyle, _ := f.NewStyle(&excelize.Style{
		Font:      &excelize.Font{Color: "0563C1", Underline: "single"},
		Alignment: &excelize.Alignment{Vertical: "center"},
	})

	// 表头
	for i, h := range xlsxHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		if err := f.SetCellValue(sheet, cell, h); err != nil {
			return err
		}
	}
	lastCol, _ := excelize.CoordinatesToCellName(len(xlsxHeaders), 1)
	if err := f.SetCellStyle(sheet, "A1", lastCol, headerStyle); err != nil {
		return err
	}

	actors := make([]*Actor, len(db.Actors))
	copy(actors, db.Actors)
	sort.Slice(actors, func(i, j int) bool {
		return NormalizeName(actors[i].Name) < NormalizeName(actors[j].Name)
	})

	for r, a := range actors {
		row := r + 2
		values := []any{
			a.Name,
			StrVal(a.NameOriginal),
			strings.Join(a.Aliases, " / "),
			a.Status,
			StrVal(a.BirthDate),
			StrVal(a.BirthPlace),
			intOrBlank(a.HeightCm),
			intOrBlank(a.WeightKg),
			StrVal(a.CupSize),
			intOrBlank(a.WaistCm),
			intOrBlank(a.HipCm),
			intOrBlank(a.CareerStart),
			trimForCell(StrVal(a.Bio), 32000),
			StrVal(a.Avatar),
			StrVal(a.AvatarPath),
			formatSources(a.Sources),
			formatProviderIDs(a.ProviderIDs),
			a.ScrapedAt,
			a.FailCount,
			a.Key,
		}
		for c, v := range values {
			cell, _ := excelize.CoordinatesToCellName(c+1, row)
			if err := f.SetCellValue(sheet, cell, v); err != nil {
				return err
			}
		}
		if avatar := StrVal(a.Avatar); avatar != "" {
			cell, _ := excelize.CoordinatesToCellName(14, row)
			if err := f.SetCellHyperLink(sheet, cell, avatar, "External"); err == nil {
				_ = f.SetCellStyle(sheet, cell, cell, linkStyle)
			}
		}
	}

	for col, w := range xlsxWidths {
		_ = f.SetColWidth(sheet, col, col, w)
	}
	_ = f.SetPanes(sheet, &excelize.Panes{
		Freeze:      true,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft",
	})
	if len(actors) > 0 {
		lastRow, _ := excelize.CoordinatesToCellName(len(xlsxHeaders), len(actors)+1)
		_ = f.AutoFilter(sheet, "A1:"+lastRow, []excelize.AutoFilterOptions{})
	}

	// 用 Write 写流，避免 SaveAs 按扩展名判断格式（.tmp 不被识别）
	tmp := path + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := f.Write(out); err != nil {
		out.Close()
		os.Remove(tmp)
		return fmt.Errorf("写入 %s 失败: %w", tmp, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func intOrBlank(p *int) any {
	if p == nil {
		return ""
	}
	return *p
}

// trimForCell 兜底：Excel 单元格上限 32767 字符。
func trimForCell(s string, max int) string {
	if len([]rune(s)) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "…"
}

func formatSources(m map[string]bool) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if m[k] {
			parts = append(parts, k+"✓")
		} else {
			parts = append(parts, k+"✗")
		}
	}
	return strings.Join(parts, " ")
}

func formatProviderIDs(m map[string]string) string {
	if len(m) == 0 {
		return ""
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		if m[k] != "" {
			parts = append(parts, k+":"+m[k])
		}
	}
	return strings.Join(parts, "; ")
}
