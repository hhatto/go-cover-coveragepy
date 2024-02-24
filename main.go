package main

import (
	"bufio"
	"embed"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	textTemplate "text/template"
	"time"

	"golang.org/x/mod/modfile"
)

type CoverageResult struct {
	Module      string
	StartLine   uint
	StartColumn uint
	EndLine     uint
	EndColumn   uint
	Reached     bool
}

type CoverRange struct {
	Start uint
	End   uint
}

type Item struct {
	Percentage    uint // 0-100
	Reached       uint
	Missed        uint
	Excluded      uint
	Statement     uint // reached + missed
	ReachedRanges []CoverRange
	MissedRanges  []CoverRange
	All           uint
	DisplayFile   string
	HtmlLink      string
}

func (item *Item) IsReached(num uint) bool {
	for _, rangeItem := range item.ReachedRanges {
		if rangeItem.Start <= num && rangeItem.End >= num {
			return true
		}
	}
	return false
}

func (item *Item) IsMissed(num uint) bool {
	for _, rangeItem := range item.MissedRanges {
		if rangeItem.Start <= num && rangeItem.End >= num {
			return true
		}
	}
	return false
}

type LineItem struct {
	Text string
	Type string // run, pln, `mis show_mis`

}

type TotalItem struct {
	Percentage uint // 0-100
	Reached    uint
	Missed     uint
	Excluded   uint
	All        uint
}

type Summary struct {
	Mode      string
	Total     TotalItem
	Items     []*Item
	CreatedAt *time.Time
}

type FileSummary struct {
	Item      *Item
	Lines     []*LineItem
	CreatedAt *time.Time
}

//go:embed templates
var f embed.FS

var logger *slog.Logger

func templateStrftime(t *time.Time) string {
	return t.Format("2006-01-02 15:04 -07:00")
}

// flatten filename
//
// * github.com/user/repo/file.go -> github_com_user_repo_file_go
func flattenFilename(filename string) string {
	base := strings.ReplaceAll(filename, ".", "_")
	base = strings.ReplaceAll(base, "/", "_")
	return base
}

func parseGoMod(path string) string {
	gomod := filepath.Join(path, "go.mod")
	data, err := os.ReadFile(gomod)
	if err != nil {
		panic(err)
	}

	modFile, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		panic(err)
	}

	packageName := modFile.Module.Mod.Path

	return packageName
}

func getLines(filename string) ([]string, error) {
	lines := make([]string, 0)
	file, err := os.Open(filename)
	if err != nil {
		return lines, err
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	return lines, nil
}

func writeTemplateFile(tmpl *template.Template, filename string, data interface{}) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	err = tmpl.Execute(file, data)
	if err != nil {
		return err
	}
	return nil
}

func writeTextTemplateFile(tmpl *textTemplate.Template, filename string, data interface{}) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer file.Close()
	err = tmpl.Execute(file, data)
	if err != nil {
		return err
	}
	return nil
}

func writeFiles(outputDir string, packageName string, items map[string]*Item, summary *Summary) error {
	if _, err := os.Stat(outputDir); os.IsNotExist(err) {
		if err := os.Mkdir(outputDir, 0755); err != nil {
			return err
		}
	}

	funcMap := template.FuncMap{
		"add": func(a int, b int) int {
			return a + b
		},
		"strftime": templateStrftime,
		"getProgressBarBgColor": func(percentage uint) string {
			if percentage < 30 {
				return "bg-danger"
			} else if percentage < 70 {
				return "bg-warning"
			}
			return "bg-success"
		},
	}

	// write index.html
	tmplIndex, err := template.New("index.html").Funcs(funcMap).ParseFS(f, "templates/index.html")
	if err != nil {
		return err
	}
	if err := writeTemplateFile(tmplIndex, filepath.Join(outputDir, "index.html"), summary); err != nil {
		return err
	}

	tmplFile, err := template.New("file.html").Funcs(funcMap).ParseFS(f, "templates/file.html")
	if err != nil {
		return err
	}

	// write files
	for _, v := range items {
		var lineItems []*LineItem
		var filename string
		{
			tmp := strings.Split(v.DisplayFile, packageName)
			if len(tmp) > 1 {
				filename = tmp[1]
			} else {
				filename = v.DisplayFile
			}
			if filename[0] == '/' {
				filename = filename[1:]
			}
		}
		lines, err := getLines(filename)
		if err != nil {
			return err
		}
		for idx, line := range lines {
			coverType := "pln"
			if v.IsReached(uint(idx + 1)) {
				coverType = "run"
			} else if v.IsMissed(uint(idx + 1)) {
				coverType = "mis show_mis"
			}
			logger.Debug("file.reach", "reach", v.ReachedRanges, "miss", v.MissedRanges, "idx", idx, "line", line, "type", coverType)
			lineItems = append(lineItems, &LineItem{
				Text: line,
				Type: coverType,
			})
		}

		if err := writeTemplateFile(tmplFile, filepath.Join(outputDir, v.HtmlLink), &FileSummary{
			Item:      v,
			Lines:     lineItems,
			CreatedAt: summary.CreatedAt,
		}); err != nil {
			return err
		}
	}

	// js, css, and more...
	styleFiles := []string{
		"coverage_html.js",
		"style.css",
		"bootstrap.min.css",
		"bootstrap.bundle.min.js",
	}
	for _, styleFile := range styleFiles {
		tmplStyle, err := textTemplate.ParseFS(f, "templates/"+styleFile)
		if err != nil {
			return err
		}
		if err := writeTextTemplateFile(tmplStyle, filepath.Join(outputDir, styleFile), nil); err != nil {
			return err
		}
	}

	// .gitignore
	file, err := os.Create(filepath.Join(outputDir, ".gitignore"))
	if err != nil {
		return err
	}
	if _, err := file.WriteString("*\n"); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}

	return nil
}

func main() {
	level := new(slog.LevelVar)
	level.Set(slog.LevelInfo)
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	logger = slog.New(handler)

	if len(os.Args) < 2 {
		fmt.Println("usage: go run main.go <file>")
		return
	}
	filename := os.Args[1]

	file, err := os.Open(filename)
	if err != nil {
		fmt.Println("usage: go run main.go <file>")
		return
	}
	defer file.Close()

	basePath := filepath.Dir(filename)
	packageName := parseGoMod(basePath)
	logger.Debug("package name", "name", packageName)

	scanner := bufio.NewScanner(file)

	// skip the first line
	scanner.Scan()
	firstLine := scanner.Text()
	// NOTE: mode is not used, now
	modeStr := strings.Split(firstLine, "mode: ")
	mode := modeStr[1]

	coverResults := make([]*CoverageResult, 0)
	for scanner.Scan() {
		line := scanner.Text()
		words := strings.Split(line, " ")
		infos := strings.Split(words[0], ":")
		module, startEnd := infos[0], infos[1]
		startEnds := strings.Split(startEnd, ",")
		reached := words[2]
		logger.Debug("cover result", "module", module, "start", startEnds[0], "end", startEnds[1], "reached", reached)

		s := strings.Split(startEnds[0], ".")
		e := strings.Split(startEnds[1], ".")
		startLine, err := strconv.Atoi(s[0])
		if err != nil {
			fmt.Println("error occurred:", err)
			return
		}
		startColumn, err := strconv.Atoi(s[1])
		if err != nil {
			fmt.Println("error occurred:", err)
			return
		}
		endLine, err := strconv.Atoi(e[0])
		if err != nil {
			fmt.Println("error occurred:", err)
			return
		}
		endColumn, err := strconv.Atoi(e[1])
		if err != nil {
			fmt.Println("error occurred:", err)
			return
		}

		cov := &CoverageResult{
			Module:      module,
			StartLine:   uint(startLine),
			StartColumn: uint(startColumn),
			EndLine:     uint(endLine),
			EndColumn:   uint(endColumn),
			Reached:     reached == "1",
		}

		coverResults = append(coverResults, cov)
	}

	// summarize
	items := make(map[string]*Item)
	var lastModule string
	var lastCov CoverageResult
	var reachedNum, totalReachedNum uint
	var missedNum, totalMissedNum uint
	var totalStatementNum uint
	var reachedRanges, missedRanges []CoverRange
	// var excludedNum, totalExcludedNum uint
	var allNum, totalAllNum uint
	for _, cov := range coverResults {
		if lastModule == "" {
			if cov.Reached {
				reachedNum += cov.EndLine - cov.StartLine
				reachedRanges = append(reachedRanges, CoverRange{cov.StartLine, cov.EndLine})
			} else {
				missedNum += cov.EndLine - cov.StartLine
				missedRanges = append(missedRanges, CoverRange{cov.StartLine, cov.EndLine})
			}
			lastModule = cov.Module
			items[cov.Module] = &Item{}

			items[lastModule].Reached = reachedNum
			items[lastModule].Missed = missedNum
			items[lastModule].Statement = reachedNum + missedNum
			items[lastModule].All = allNum
			// items[lastModule].Excluded = allNum - reachedNum - missedNum
			items[lastModule].Percentage = uint(math.Ceil(float64(reachedNum) / float64(reachedNum+missedNum) * 100))
			items[lastModule].DisplayFile = lastModule
			items[lastModule].HtmlLink = flattenFilename(lastModule) + ".html"

			totalReachedNum += reachedNum
			totalMissedNum += missedNum
			totalAllNum += allNum
		} else if lastModule != "" && lastModule != cov.Module {
			// for old module
			allNum = lastCov.EndLine
			if !lastCov.Reached {
				allNum += 1
			}
			// reachedNum = allNum - missedNum
			items[lastModule].Reached = reachedNum
			items[lastModule].Missed = missedNum
			items[lastModule].Statement = reachedNum + missedNum
			items[lastModule].All = allNum
			// items[lastModule].Excluded = allNum - reachedNum - missedNum
			items[lastModule].Percentage = uint(math.Ceil(float64(reachedNum) / float64(reachedNum+missedNum) * 100))
			items[lastModule].DisplayFile = lastModule
			items[lastModule].HtmlLink = flattenFilename(lastModule) + ".html"
			items[lastModule].ReachedRanges = reachedRanges
			items[lastModule].MissedRanges = missedRanges

			logger.Debug("summary", "module", lastModule, "start", reachedNum, "end", missedNum)

			totalReachedNum += reachedNum
			totalMissedNum += missedNum
			totalStatementNum += reachedNum + missedNum
			totalAllNum += allNum

			reachedNum = 0
			missedNum = 0
			reachedRanges = make([]CoverRange, 0)
			missedRanges = make([]CoverRange, 0)

			// for new module
			items[cov.Module] = &Item{}

			if cov.Reached {
				reachedNum += cov.EndLine - cov.StartLine
				reachedRanges = append(reachedRanges, CoverRange{cov.StartLine, cov.EndLine})
			} else {
				missedNum += cov.EndLine - cov.StartLine
				missedRanges = append(missedRanges, CoverRange{cov.StartLine, cov.EndLine})
			}
		} else {
			if cov.Reached {
				reachedNum += cov.EndLine - cov.StartLine
				reachedRanges = append(reachedRanges, CoverRange{cov.StartLine, cov.EndLine})
			} else {
				missedNum += cov.EndLine - cov.StartLine
				missedRanges = append(missedRanges, CoverRange{cov.StartLine, cov.EndLine})
			}
		}
		lastModule = cov.Module
		lastCov = *cov
	}

	// care of last item
	allNum = lastCov.EndLine
	if !lastCov.Reached {
		allNum += 1
	}
	// reachedNum = allNum - missedNum
	items[lastModule].Reached = reachedNum
	items[lastModule].Missed = missedNum
	items[lastModule].Statement = reachedNum + missedNum
	items[lastModule].All = allNum
	// items[lastModule].Excluded = allNum - reachedNum - missedNum
	items[lastModule].Percentage = uint(math.Ceil(float64(reachedNum) / float64(reachedNum+missedNum) * 100.))
	items[lastModule].DisplayFile = lastModule
	items[lastModule].HtmlLink = flattenFilename(lastModule) + ".html"
	if lastCov.Reached {
		reachedNum += lastCov.EndLine - lastCov.StartLine
		reachedRanges = append(reachedRanges, CoverRange{lastCov.StartLine, lastCov.EndLine})
	} else {
		missedNum += lastCov.EndLine - lastCov.StartLine
		missedRanges = append(missedRanges, CoverRange{lastCov.StartLine, lastCov.EndLine})
	}
	items[lastModule].ReachedRanges = reachedRanges
	items[lastModule].MissedRanges = missedRanges
	logger.Debug("last", "module", lastModule, "reach", reachedNum, "missed", missedNum, "all", allNum)
	logger.Debug("last.percentage", "percentage", uint(math.Ceil(float64(reachedNum)/float64(reachedNum+missedNum)*100.)))

	totalReachedNum += reachedNum
	totalMissedNum += missedNum
	totalStatementNum += reachedNum + missedNum
	totalAllNum += allNum

	logger.Debug("total", "reach", totalReachedNum, "missed", totalMissedNum, "all", totalAllNum)

	summaryItems := make([]*Item, 0, len(items))
	for _, v := range items {
		summaryItems = append(summaryItems, v)
	}

	sortFunc := func(i, j int) bool {
		return summaryItems[i].DisplayFile < summaryItems[j].DisplayFile
	}
	sort.Slice(summaryItems, sortFunc)

	now := time.Now()
	summary := &Summary{
		Mode: mode,
		Total: TotalItem{
			All:        totalAllNum,
			Reached:    totalReachedNum,
			Missed:     totalMissedNum,
			Excluded:   totalAllNum - totalReachedNum - totalMissedNum,
			Percentage: uint(math.Ceil(float64(totalReachedNum) / float64(totalStatementNum) * 100)),
		},
		Items:     summaryItems,
		CreatedAt: &now,
	}
	outputDir := "htmlcov"
	if err := writeFiles(outputDir, packageName, items, summary); err != nil {
		os.Exit(1)
	}

	if err := scanner.Err(); err != nil {
		fmt.Println("error occured:", err)
	}
}
