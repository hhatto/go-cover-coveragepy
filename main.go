package main

import (
	"bufio"
	"context"
	"embed"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	textTemplate "text/template"
	"time"

	"golang.org/x/mod/modfile"
)

// functions for template
var funcMap template.FuncMap = template.FuncMap{
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

type CoverageResult struct {
	Module      string
	StartLine   uint
	StartColumn uint
	EndLine     uint
	EndColumn   uint
	StmtCount   uint
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
	Statement  uint
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

func writeStaticFiles(outputDir string) error {
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

func writeIndexFile(outputDir string, summary *Summary) error {
	// write index.html
	tmplIndex, err := template.New("index.html").Funcs(funcMap).ParseFS(f, "templates/index.html")
	if err != nil {
		return err
	}
	if err := writeTemplateFile(tmplIndex, filepath.Join(outputDir, "index.html"), summary); err != nil {
		return err
	}
	return nil
}

func writeProfileFile(tmplFile *template.Template, outputFilename, packageName string, item *Item, createdAt *time.Time) error {
	var lineItems []*LineItem
	var filename string
	{
		tmp := strings.Split(item.DisplayFile, packageName)
		if len(tmp) > 1 {
			filename = tmp[1]
		} else {
			filename = item.DisplayFile
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
		if item.IsReached(uint(idx + 1)) {
			coverType = "run"
		} else if item.IsMissed(uint(idx + 1)) {
			coverType = "mis show_mis"
		}
		logger.Debug("file.reach", "reach", item.ReachedRanges, "miss", item.MissedRanges, "idx", idx, "line", line, "type", coverType)
		lineItems = append(lineItems, &LineItem{
			Text: line,
			Type: coverType,
		})
	}

	if err := writeTemplateFile(tmplFile, outputFilename, &FileSummary{
		Item:      item,
		Lines:     lineItems,
		CreatedAt: createdAt,
	}); err != nil {
		return err
	}

	return nil
}

type WorkerProcessRequest struct {
	tmplFile       *template.Template
	outputFilename string
	packageName    string
	item           *Item
}

func startWorker(ctx context.Context, wg *sync.WaitGroup, num int) (requestch chan *WorkerProcessRequest) {
	requestch = make(chan *WorkerProcessRequest)

	for i := 0; i < num; i++ {
		go func() {
			for {
				select {
				case req := <-requestch:
					logger.Debug("worker", "path", req.outputFilename)
					now := time.Now()
					if err := writeProfileFile(req.tmplFile, req.outputFilename, req.packageName, req.item, &now); err != nil {
						logger.Error("write profile file error", "error", err)
					}
					wg.Done()
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	return
}

func main() {
	helpFlag := flag.Bool("h", false, "Show help")
	debugFlag := flag.Bool("d", false, "Enable debug mode")
	outputDir := flag.String("o", "htmlcov", "Output directory")
	jobs := flag.Int("jobs", 4, "Number of jobs")

	flag.Parse()

	if *helpFlag {
		fmt.Printf("Usage: go run main.go [-d] [-o <output directory>] <COVER_FILE>\n\n")
		flag.PrintDefaults()
		return
	}

	if flag.NArg() < 1 {
		fmt.Printf("Usage: go run main.go [-d] [-o <output directory>] <COVER_FILE>\n\n")
		flag.PrintDefaults()
		return
	}

	filename := flag.Arg(0)

	level := new(slog.LevelVar)
	if *debugFlag {
		level.Set(slog.LevelDebug)
	} else {
		level.Set(slog.LevelInfo)
	}
	handler := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})
	logger = slog.New(handler)

	file, err := os.Open(filename)
	if err != nil {
		fmt.Println("file opne error:", err)
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
		stmtCountStr := words[1]
		reached := words[2]
		logger.Debug("cover result", "module", module, "start", startEnds[0], "end", startEnds[1], "stmt", stmtCountStr, "reached", reached)

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
		stmtCount, err := strconv.Atoi(stmtCountStr)
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
			StmtCount:   uint(stmtCount),
			Reached:     reached != "0",
		}

		coverResults = append(coverResults, cov)
	}

	if _, err := os.Stat(*outputDir); os.IsNotExist(err) {
		if err := os.Mkdir(*outputDir, 0755); err != nil {
			fmt.Println("error occurred:", err)
			os.Exit(1)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wg := &sync.WaitGroup{}
	worker := startWorker(ctx, wg, *jobs)

	tmplFile, err := template.New("file.html").Funcs(funcMap).ParseFS(f, "templates/file.html")
	if err != nil {
		fmt.Println("error occurred:", err)
		os.Exit(1)
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
			// first cover line
			if cov.Reached {
				reachedNum += cov.StmtCount
				reachedRanges = append(reachedRanges, CoverRange{cov.StartLine, cov.EndLine})
			} else {
				missedNum += cov.StmtCount
				missedRanges = append(missedRanges, CoverRange{cov.StartLine, cov.EndLine})
			}
			lastModule = cov.Module
			items[cov.Module] = &Item{}

			items[lastModule].Reached = reachedNum
			items[lastModule].Missed = missedNum
			items[lastModule].Statement = reachedNum + missedNum
			items[lastModule].All = allNum
			items[lastModule].Percentage = uint(math.Round(float64(reachedNum) / float64(reachedNum+missedNum) * 100))
			items[lastModule].DisplayFile = lastModule
			items[lastModule].HtmlLink = flattenFilename(lastModule) + ".html"

			totalReachedNum += reachedNum
			totalMissedNum += missedNum
			totalAllNum += allNum
		} else if lastModule != "" && lastModule != cov.Module {
			// for old module
			items[lastModule].Reached = reachedNum
			items[lastModule].Missed = missedNum
			items[lastModule].Statement = reachedNum + missedNum
			items[lastModule].All = allNum
			items[lastModule].Percentage = uint(math.Ceil(float64(reachedNum) / float64(reachedNum+missedNum) * 100))
			items[lastModule].DisplayFile = lastModule
			items[lastModule].HtmlLink = flattenFilename(lastModule) + ".html"
			items[lastModule].ReachedRanges = reachedRanges
			items[lastModule].MissedRanges = missedRanges

			logger.Debug("summary", "module", lastModule, "start", reachedNum, "end", missedNum)

			wg.Add(1)
			worker <- &WorkerProcessRequest{
				tmplFile:       tmplFile,
				outputFilename: filepath.Join(*outputDir, items[lastModule].HtmlLink),
				packageName:    packageName,
				item:           items[lastModule],
			}

			allNum = cov.StmtCount
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
				reachedNum += cov.StmtCount
				reachedRanges = append(reachedRanges, CoverRange{cov.StartLine, cov.EndLine})
			} else {
				missedNum += cov.StmtCount
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
	items[lastModule].Percentage = uint(math.Round(float64(reachedNum) / float64(reachedNum+missedNum) * 100.))
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
	logger.Debug("last.percentage", "percentage", uint(math.Round(float64(reachedNum)/float64(reachedNum+missedNum)*100.)))

	wg.Add(1)
	worker <- &WorkerProcessRequest{
		tmplFile:       tmplFile,
		outputFilename: filepath.Join(*outputDir, items[lastModule].HtmlLink),
		packageName:    packageName,
		item:           items[lastModule],
	}

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
			Statement:  totalStatementNum,
			Reached:    totalReachedNum,
			Missed:     totalMissedNum,
			Excluded:   totalAllNum - totalReachedNum - totalMissedNum,
			Percentage: uint(math.Round(float64(totalReachedNum) / float64(totalStatementNum) * 100)),
		},
		Items:     summaryItems,
		CreatedAt: &now,
	}

	if err := writeIndexFile(*outputDir, summary); err != nil {
		fmt.Println("error occurred:", err)
		os.Exit(1)
	}

	if err := writeStaticFiles(*outputDir); err != nil {
		fmt.Println("error occurred:", err)
		os.Exit(1)
	}

	wg.Wait()

	if err := scanner.Err(); err != nil {
		fmt.Println("error occured:", err)
	}
}
