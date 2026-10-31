/*
usage: go run ./cmd/topomaker --zoom 5 -h 400 -w 400 --hill 250 --hill-wide 80 --ridge 5 --ridge-len 20 --ridge-wide 50 --color-tpl-step 40 --stuck 6 --petal-shape 2 --petal-num 3 --draw-flag 6 --dropnum 100000 --times 50 --seed 1708692666

参数与 legacy/topomaker.go 一致，另外新增 -v -workers -fix-field -fix-middle -snapshot -resume。
*/
package main

import (
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"time"

	"topogen/internal/engine"
)

func main() {
	nowTs := time.Now()
	p := engine.DefaultParams()

	flag.IntVar(&p.Width, "w", p.Width, "width of map")
	flag.IntVar(&p.Length, "h", p.Length, "length of map")
	outname := flag.String("out", "topomap", "image filename of output")
	outdir := flag.String("outdir", "output", "out put dir")

	flag.IntVar(&p.Hills, "hill", p.Hills, "hill number for making rand topo by hill")
	flag.IntVar(&p.HillWide, "hill-wide", p.HillWide, "hill wide for making rand topo by hill")
	flag.IntVar(&p.Ridges, "ridge", p.Ridges, "num of ridges for making ridges")
	flag.IntVar(&p.RidgeWide, "ridge-wide", p.RidgeWide, "ridge wide for making ridge each")
	flag.IntVar(&p.RidgeLen, "ridge-len", p.RidgeLen, "ridge length when making ridge each")
	flag.IntVar(&p.Stuck, "stuck", p.Stuck, "stuck hill number, hill in stuck area will pressed, even length be 0")
	flag.IntVar(&p.Petal.Shape, "petal-shape", p.Petal.Shape, "shape of petal, 0:圆形 无花瓣 1:圆角 2:锐角")
	flag.Float64Var(&p.Petal.PetalNum, "petal-num", p.Petal.PetalNum, "petal numbers of hill")
	flag.Float64Var(&p.Petal.Sharp, "petal-sharp", p.Petal.Sharp, "petal sharp, 取值 0-1.0, 越大越锋利")
	colorTplFile := flag.String("color-tpl", "./image/color-tpl2.png", "color template file path, built-in colors are used if missing")
	flag.IntVar(&p.LowerWaterLevel, "lower", 0, "lower water level, default 0")
	flag.IntVar(&p.Blur, "blur", 0, "blur radius, default 0")
	flag.Int64Var(&p.Seed, "seed", 0, "random seed")

	flag.IntVar(&p.DropNum, "dropnum", p.DropNum, "number of drops")
	flag.IntVar(&p.Times, "times", p.Times, "update times")
	flag.IntVar(&p.Enlarge, "enlarge", 0, "enlarge scale of out put image, if enlarge=1, then the count of pixels of output image will be 2x2 times of input")

	zoom := flag.Int("zoom", 1, "zoom of out put image")
	riverArrowScale := flag.Float64("river-arrow-scale", 0.8, "river arrow scale")
	drawFlag := flag.Int("draw-flag", engine.DrawFlagDroplet, "draw flag: 1=field 2=droplet 4=catchment 8=qflow 16=debug droplet 32=topo+catchment 64=topo-catchment")
	colorTplStep := flag.Int("color-tpl-step", 0, "color tpl file step line, will igore there step in tpl")

	basemap := flag.String("basemap", "", "base map file path")
	basemapTone := flag.String("basemap-tone", "0", "0=light is high, 1=dark is high, r=red is high, g=green is high, b=blue is high")
	basemapCover := flag.String("basemap-cover", "", "options: sum,avg,max,shape")

	flag.BoolVar(&p.Verbose, "v", false, "print per-event debug logs (slow)")
	flag.IntVar(&p.Workers, "workers", 0, "goroutines for moving droplets, 0 = all CPUs")
	flag.BoolVar(&p.Compat.FixFieldInit, "fix-field", false, "fix: really assign the initial field vectors (changes results)")
	flag.BoolVar(&p.Compat.FixMiddleIdx, "fix-middle", false, "fix: use middleIdx when eroding the middle point (changes results)")
	snapshotPath := flag.String("snapshot", "", "write a snapshot to this file when finished")
	resumePath := flag.String("resume", "", "resume from a snapshot file instead of generating terrain")
	flag.Parse()

	if p.Seed == 0 {
		p.Seed = nowTs.Unix()
	}

	if *basemap != "" {
		img, err := loadImage(*basemap)
		if err != nil {
			log.Fatalf("basemap: %v", err)
		}
		p.Basemap = &engine.Basemap{Image: img, Tone: *basemapTone, Cover: *basemapCover}
		if *colorTplStep <= 0 {
			*colorTplStep = 20
		}
	}

	var sim *engine.Sim
	var err error
	if *resumePath != "" {
		sim, err = restore(*resumePath)
		if err == nil {
			p = sim.Params()
		}
	} else {
		sim, err = engine.New(p)
	}
	if err != nil {
		log.Fatal(err)
	}

	total := sim.TotalTicks()
	log.Printf("will move drops(n:%d, times:%d, enlarge:%d) total ticks:%d", p.DropNum, p.Times, p.Enlarge, total)
	lastReport := time.Now()
	for !sim.Done() {
		sim.Step(200)
		if time.Since(lastReport) > 5*time.Second {
			st := sim.Stats()
			log.Printf("progress %d/%d (%.1f%%) stage:%d/%d", st.Tick, st.TotalTicks, float64(st.Tick)*100/float64(max(st.TotalTicks, 1)), st.Stage, st.Stages-1)
			lastReport = time.Now()
		}
	}

	if *snapshotPath != "" {
		if err := writeSnapshot(sim, *snapshotPath); err != nil {
			log.Printf("snapshot: %v", err)
		}
	}

	st := sim.Stats()
	width, _ := sim.Size()
	log.Printf("will draw to image(zoom:%d, width:%d, length:%d) nHills=%d nRidge=%d ridgelen=%d maxLv:%d", *zoom, st.Width, st.Length, p.Hills, p.Ridges, p.RidgeLen, st.MaxLv)
	log.Printf("wMaxC,minC,maxQ=%d,%d,%d we=%d/+%d,-%d,ex:%d me=%d/+%d,-%d", st.MaxC, st.MinC, st.MaxQ, st.WaterEvents, st.WaterPlus, st.WaterMinus, st.Exchanges, st.TopoEvents, st.Sediments, st.Erosions)

	colors, err := engine.LoadColorTemplate(*colorTplFile, *colorTplStep)
	if err != nil {
		log.Printf("color template unavailable (%v), using built-in colors", err)
		colors = nil
	}
	img := sim.Render(engine.RenderOptions{Zoom: *zoom, RiverArrowScale: *riverArrowScale, DrawFlag: *drawFlag, Colors: colors})

	if err := os.MkdirAll(*outdir, os.ModePerm); err != nil {
		log.Fatalf("mkdir %s: %v", *outdir, err)
	}
	tnTag := ""
	if *basemap != "" {
		tnTag = "-tn" + *basemapTone
	}
	widthOri := width >> p.Enlarge
	timeUsed := time.Since(nowTs).Milliseconds() / 1000
	out := filepath.Join(*outdir, fmt.Sprintf("%s-%s-s%d-w%d-hi%d-hw%d-r%d-rl%d-rw%d-st%d-dr%d-t%d-e%d%s.%ds.r8.png",
		*outname, time.Now().Format("20060102150405"), p.Seed, widthOri, p.Hills, p.HillWide, p.Ridges, p.RidgeLen, p.RidgeWide, p.Stuck, p.DropNum, p.Times, p.Enlarge, tnTag, timeUsed))
	if err := writePNG(out, img); err != nil {
		log.Fatal(err)
	}
	log.Printf("image saved: %s", out)

	if *drawFlag&engine.DrawFlagDebugDroplet > 0 {
		for di, d := range sim.Droplets() {
			log.Printf("drops[%d]=%+v", di, d)
		}
	}
	ds := sim.DropStats()
	log.Printf("stayLongCount=%d maxJourney=%d avgJourney=%d upDotNum=%d downDotNum=%d seed=%d", st.StayLong, ds.MaxJourney, ds.AvgJourney, ds.UpDotNum, ds.DownDotNum, p.Seed)
}

func loadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, format, err := image.Decode(f) // only support png and jpg
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	log.Printf("basemap parsed: format:%s bounds: %+v", format, img.Bounds())
	return img, nil
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func writeSnapshot(sim *engine.Sim, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := sim.Snapshot(f); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func restore(path string) (*engine.Sim, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return engine.Restore(f)
}
