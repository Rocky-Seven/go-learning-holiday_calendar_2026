// main.go (祝日対応カレンダー2026年：ブラウザ上で日だけ表示、祝日は日付の下に表示)
package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/transform"
)

const sourceURL = "https://www8.cao.go.jp/chosei/shukujitsu/syukujitsu.csv"
const outCSV = "holidays_2026.csv"

var tpl = template.Must(template.New("cal").Parse(`<!doctype html>
<html lang="ja">
<head>
<meta charset="utf-8" />
<title>2026年カレンダー（Go言語）</title>
<meta name="viewport" content="width=device-width,initial-scale=1" />
<style>
  body{font-family:Segoe UI, Meiryo, "Hiragino Kaku Gothic ProN", sans-serif; background:#f7f7fb; padding:18px;}
  .wrap{max-width:920px;margin:0 auto;background:#fff;padding:18px;border-radius:10px;box-shadow:0 6px 18px rgba(0,0,0,0.06);}
  .header{display:flex;justify-content:space-between;align-items:center;margin-bottom:12px;}
  h1{margin:0;font-size:20px;}
  .nav a{margin-left:8px;text-decoration:none;padding:6px 10px;border-radius:6px;background:#125;color:#fff;}
  table.calendar{width:100%;border-collapse:collapse;}
  table.calendar th{background:#f0f0f4;padding:8px;font-weight:700;border:1px solid #e6e6e6;}
  table.calendar td{width:14.285%;vertical-align:top;border:1px solid #e6e6e6;padding:8px;height:110px;}
  .cell {display:flex;flex-direction:column;align-items:flex-start;gap:6px;height:100%;}
  .daynum{font-weight:700;font-size:1.05rem;}
  .holidayname{margin-top:6px;font-size:0.95rem;color:#b22222;}
  .sunday{background:#fff7f7;color:#b22222;}
  .saturday{background:#f4fbff;color:#1b69a8;}
  .footer{margin-top:12px;color:#666;font-size:0.9rem;}
  .controls{display:flex;gap:8px;align-items:center;}
  a.btn{background:#2d7;padding: color:#fff;}
  @media (max-width:600px){
    table.calendar td{height:86px;padding:6px;}
  }
</style>
</head>
<body>
<div class="wrap">
  <div class="header">
    <h1>2026年カレンダー（Go言語） — {{.Month}}月</h1>
    <div class="controls">
      {{if .PrevMonthURL}}<a class="nav btn" href="{{.PrevMonthURL}}">◀ 前の月</a>{{end}}
      {{if .NextMonthURL}}<a class="nav btn" href="{{.NextMonthURL}}">次の月 ▶</a>{{end}}
      <a class="nav btn" href="/">今月へ</a>
    </div>
  </div>

  <table class="calendar" role="table" aria-label="2026 calendar">
    <thead>
      <tr>
        <th>日</th><th>月</th><th>火</th><th>水</th><th>木</th><th>金</th><th>土</th>
      </tr>
    </thead>
    <tbody>
      {{range .Weeks}}
      <tr>
        {{range .}}
          {{if .Zero}}
            <td></td>
          {{else}}
            {{ $wd := .T.Weekday }}
            <td class="{{if eq $wd 0}}sunday{{else if eq $wd 6}}saturday{{end}}">
              <div class="cell">
                <div class="daynum">{{.Day}}</div>
                {{if .Holiday}}
                  <div class="holidayname">{{.Holiday}}</div>
                {{end}}
                <div style="margin-top:auto;"></div>
              </div>
            </td>
          {{end}}
        {{end}}
      </tr>
      {{end}}
    </tbody>
  </table>

  <div class="footer">
    CSV: {{.CSVPath}} | 年: {{.Year}} | 表示月: {{.Month}}
  </div>
</div>
</body>
</html>
`))

type Cell struct {
	Zero    bool
	T       time.Time
	Day     int
	Holiday string
}

type Page struct {
	Year        int
	Month       int
	Weeks       [][]Cell
	CSVPath     string
	PrevMonthURL string
	NextMonthURL string
}

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	csvPath := flag.String("csv", outCSV, "出力CSVパス（UTF-8, 2026年分）")
	force := flag.Bool("force", false, "既存ファイルがあっても再ダウンロードする場合は true")
	flag.Parse()

	if *force || !fileExists(*csvPath) {
		log.Printf("downloading and extracting 2026 holidays -> %s", *csvPath)
		if err := downloadAndExtract2026(sourceURL, *csvPath); err != nil {
			log.Fatalf("download/extract failed: %v", err)
		}
	} else {
		log.Printf("using existing %s (use -force to re-download)", *csvPath)
	}

	holMap, err := loadHolidays(*csvPath)
	if err != nil {
		log.Fatalf("failed to load holidays: %v", err)
	}

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		year := 2026
		month := 1
		now := time.Now()
		if now.Year() == 2026 {
			month = int(now.Month())
		}

		if ms := r.URL.Query().Get("m"); ms != "" {
			if mi, err := strconv.Atoi(ms); err == nil && mi >= 1 && mi <= 12 {
				month = mi
			}
		}

		p := buildPage(year, month, holMap, *csvPath)
		if err := tpl.Execute(w, p); err != nil {
			log.Printf("template exec: %v", err)
		}
	})

	log.Printf("server start at http://localhost%s/ (CSV: %s)\n", *addr, *csvPath)
	log.Fatal(http.ListenAndServe(*addr, nil))
}

// downloadAndExtract2026 downloads source CSV, decodes Shift_JIS->UTF-8,
// extracts rows year==2026 and writes outPath in UTF-8 with date formatted as YYYY-MM-DD
func downloadAndExtract2026(sourceURL, outPath string) error {
	resp, err := http.Get(sourceURL)
	if err != nil {
		return fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	utf8r := transform.NewReader(resp.Body, japanese.ShiftJIS.NewDecoder())
	r := csv.NewReader(utf8r)
	r.FieldsPerRecord = -1

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create out file: %w", err)
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()

	formats := []string{"2006/1/2", "2006/01/02", "2006-01-02", "2006/1/2 0:00:00"}

	for {
		rec, err := r.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("csv read warning: %v", err)
			continue
		}
		if len(rec) == 0 {
			continue
		}
		for i := range rec {
			rec[i] = strings.TrimSpace(rec[i])
			if i == 0 {
				rec[i] = strings.TrimPrefix(rec[i], "\uFEFF")
			}
		}
		dateStr := rec[0]
		var dt time.Time
		var perr error
		for _, fm := range formats {
			dt, perr = time.Parse(fm, dateStr)
			if perr == nil {
				break
			}
		}
		if perr != nil {
			continue
		}
		if dt.Year() != 2026 {
			continue
		}
		name := ""
		if len(rec) >= 2 {
			name = rec[1]
		}
		outRec := []string{dt.Format("2006-01-02"), name}
		if err := w.Write(outRec); err != nil {
			return fmt.Errorf("write csv: %w", err)
		}
	}

	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("csv writer error: %w", err)
	}
	return nil
}

// loadHolidays reads UTF-8 CSV and returns map keyed by "2006-01-02" -> name
func loadHolidays(path string) (map[string]string, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1
	hol := make(map[string]string)
	formats := []string{"2006-01-02", "2006/01/02", "2006/1/2"}

	for {
		rec, err := r.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		if len(rec) < 1 {
			continue
		}
		for i := range rec {
			rec[i] = strings.TrimSpace(rec[i])
			if i == 0 {
				rec[i] = strings.TrimPrefix(rec[i], "\uFEFF")
			}
		}
		dateStr := rec[0]
		name := ""
		if len(rec) >= 2 {
			name = rec[1]
		}
		var dt time.Time
		var perr error
		for _, fm := range formats {
			dt, perr = time.Parse(fm, dateStr)
			if perr == nil {
				break
			}
		}
		if perr != nil {
			continue
		}
		if name == "" {
			name = "祝日"
		}
		hol[dt.Format("2006-01-02")] = name
	}
	return hol, nil
}

func buildPage(year, month int, hol map[string]string, csvPath string) Page {
	loc := time.Local
	first := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, loc)
	startW := int(first.Weekday())

	var nextMonth time.Time
	if month == 12 {
		nextMonth = time.Date(year+1, 1, 1, 0, 0, 0, 0, loc)
	} else {
		nextMonth = time.Date(year, time.Month(month+1), 1, 0, 0, 0, 0, loc)
	}
	days := int(nextMonth.AddDate(0, 0, -1).Day())

	var weeks [][]Cell
	week := make([]Cell, 0, 7)
	for i := 0; i < startW; i++ {
		week = append(week, Cell{Zero: true})
	}
	for d := 1; d <= days; d++ {
		t := time.Date(year, time.Month(month), d, 0, 0, 0, 0, loc)
		key := t.Format("2006-01-02")
		cell := Cell{
			Zero:    false,
			T:       t,
			Day:     d,
			Holiday: hol[key],
		}
		week = append(week, cell)
		if len(week) == 7 {
			weeks = append(weeks, week)
			week = make([]Cell, 0, 7)
		}
	}
	if len(week) > 0 {
		for len(week) < 7 {
			week = append(week, Cell{Zero: true})
		}
		weeks = append(weeks, week)
	}

	prevURL, nextURL := "", ""
	if month > 1 {
		prevURL = fmt.Sprintf("/?m=%d", month-1)
	}
	if month < 12 {
		nextURL = fmt.Sprintf("/?m=%d", month+1)
	}

	return Page{
		Year:         year,
		Month:        month,
		Weeks:        weeks,
		CSVPath:      csvPath,
		PrevMonthURL: prevURL,
		NextMonthURL: nextURL,
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}