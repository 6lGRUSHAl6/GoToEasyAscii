package main

import (
	"bufio"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

// рампа Пола Бурка
const bourkeRampLightToDark = " .'`^\",:;Il!i><~+_-?][}{1)(|\\/tfjrxnuvczXYUJCLQ0OZmwqpdbkhao*#MW&8%B@$"

const darkBgRamp = bourkeRampLightToDark

var lightBgRamp = reverseString(darkBgRamp)

const defaultTargetWidth = 100

func main() {
	colorFlag := flag.Bool("color", false, "цветной вывод (по умолчанию ч/б)")
	lightFlag := flag.Bool("light", false, "принудительно рампу для светлого фона")
	darkFlag := flag.Bool("dark", false, "принудительно рампу для тёмного фона")
	widthFlag := flag.Int("width", defaultTargetWidth, "ширина выходного ASCII-арта в символах")
	workersFlag := flag.Int("workers", runtime.NumCPU(), "количество параллельных воркеров (по умолчанию число CPU)")
	flag.Parse()

	args := flag.Args()
	if len(args) < 1 {
		fmt.Println("Использование: go run ascii.go [-color] [-light|-dark] [-width N] [-workers N] <путь_к_картинке>")
		return
	}

	filePath := args[0]
	file, err := os.Open(filePath)
	if err != nil {
		fmt.Printf("Ошибка открытия файла: %v\n", err)
		return
	}
	defer file.Close()

	var img image.Image
	ext := strings.ToLower(filepath.Ext(filePath))
	switch ext {
	case ".jpg", ".jpeg":
		img, err = jpeg.Decode(file)
	case ".png":
		img, err = png.Decode(file)
	default:
		img, _, err = image.Decode(file)
	}
	if err != nil {
		fmt.Printf("Ошибка декодирования: %v\n", err)
		return
	}

	bounds := img.Bounds()
	rgba := image.NewRGBA(bounds)
	draw.Draw(rgba, bounds, img, bounds.Min, draw.Src)

	targetWidth := *widthFlag
	if targetWidth < 1 {
		targetWidth = 1
	}
	scaleX := bounds.Dx() / targetWidth
	if scaleX < 1 {
		scaleX = 1
	}
	scaleY := int(float64(scaleX) * 2.1)
	if scaleY < 1 {
		scaleY = 1
	}
	cols := bounds.Dx() / scaleX

	asciiRamp := chooseRamp(*lightFlag, *darkFlag)
	rampLen := len(asciiRamp)
	ansiCache := newAnsiCache()

	var rowYs []int
	for y := bounds.Min.Y; y < bounds.Max.Y; y += scaleY {
		rowYs = append(rowYs, y)
	}
	results := make([]string, len(rowYs))
	numWorkers := *workersFlag
	if numWorkers < 1 {
		numWorkers = 1
	}
	if numWorkers > len(rowYs) {
		numWorkers = len(rowYs)
	}

	var wg sync.WaitGroup
	rowsPerWorker := (len(rowYs) + numWorkers - 1) / numWorkers

	for w := 0; w < numWorkers; w++ {
		start := w * rowsPerWorker
		end := start + rowsPerWorker
		if start >= len(rowYs) {
			break
		}
		if end > len(rowYs) {
			end = len(rowYs)
		}

		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			var sb strings.Builder
			estLen := cols
			if *colorFlag {
				estLen *= 20
			}
			sb.Grow(estLen + 10) //небольшой запас

			for i := start; i < end; i++ {
				y := rowYs[i]
				sb.Reset()

				for x := bounds.Min.X; x < bounds.Max.X; x += scaleX {
					r, g, b, brightness := averageColorAndBrightness(rgba, x, y, scaleX, scaleY)

					idx := int((brightness / 255.0) * float64(rampLen-1))
					if idx >= rampLen {
						idx = rampLen - 1
					}
					ch := asciiRamp[idx]
					if *colorFlag {
						sb.WriteString(ansiCache.get(r, g, b))
						sb.WriteByte(ch)
					} else {
						sb.WriteByte(ch)
					}
				}

				if *colorFlag {
					sb.WriteString("\x1b[0m")
				}

				results[i] = sb.String()
			}
		}(start, end)
	}

	wg.Wait()

	writer := bufio.NewWriter(os.Stdout)
	defer func() {
		if err := writer.Flush(); err != nil {
			fmt.Fprintf(os.Stderr, "Ошибка сброса буфера: %v\n", err)
		}
	}()

	for _, line := range results {
		if _, err := writer.WriteString(line); err != nil {
			fmt.Printf("Ошибка записи: %v\n", err)
			return
		}
		if err := writer.WriteByte('\n'); err != nil {
			fmt.Printf("Ошибка записи: %v\n", err)
			return
		}
	}
}

func chooseRamp(lightFlag, darkFlag bool) string {
	if lightFlag {
		return lightBgRamp
	}
	if darkFlag {
		return darkBgRamp
	}

	if val := os.Getenv("COLORFGBG"); val != "" {
		parts := strings.Split(val, ";")
		if len(parts) > 0 {
			bg, err := strconv.Atoi(parts[len(parts)-1])
			if err == nil {
				if bg == 7 || bg >= 9 {
					return lightBgRamp
				}
				return darkBgRamp
			}
		}
	}
	return darkBgRamp //по умолчанию тёмный фон
}

func reverseString(s string) string {
	runes := []rune(s)
	for i, j := 0, len(runes)-1; i < j; i, j = i+1, j-1 {
		runes[i], runes[j] = runes[j], runes[i]
	}
	return string(runes)
}

type ansiCache struct {
	mu    sync.RWMutex
	cache map[[3]uint8]string
}

func newAnsiCache() *ansiCache {
	return &ansiCache{cache: make(map[[3]uint8]string)}
}

func (a *ansiCache) get(r, g, b uint8) string {
	key := [3]uint8{r, g, b}

	a.mu.RLock()
	if v, ok := a.cache[key]; ok {
		a.mu.RUnlock()
		return v
	}
	a.mu.RUnlock()

	//сборка строки
	var sb strings.Builder
	sb.WriteString("\x1b[38;2;")
	sb.WriteString(strconv.Itoa(int(r)))
	sb.WriteByte(';')
	sb.WriteString(strconv.Itoa(int(g)))
	sb.WriteByte(';')
	sb.WriteString(strconv.Itoa(int(b)))
	sb.WriteByte('m')
	v := sb.String()
	a.mu.Lock()
	if existing, ok := a.cache[key]; ok {
		a.mu.Unlock()
		return existing
	}
	a.cache[key] = v
	a.mu.Unlock()
	return v
}

func averageColorAndBrightness(img *image.RGBA, x, y, w, h int) (r, g, b uint8, brightness float64) {
	var sumR, sumG, sumB float64
	var count int

	maxX := x + w
	if maxX > img.Bounds().Max.X {
		maxX = img.Bounds().Max.X
	}
	maxY := y + h
	if maxY > img.Bounds().Max.Y {
		maxY = img.Bounds().Max.Y
	}

	for yy := y; yy < maxY; yy++ {
		rowOffset := yy * img.Stride
		for xx := x; xx < maxX; xx++ {
			offset := rowOffset + xx*4
			sumR += float64(img.Pix[offset])
			sumG += float64(img.Pix[offset+1])
			sumB += float64(img.Pix[offset+2])
			count++
		}
	}

	if count == 0 {
		return 0, 0, 0, 0
	}
	avgR := sumR / float64(count)
	avgG := sumG / float64(count)
	avgB := sumB / float64(count)
	r = uint8(avgR)
	g = uint8(avgG)
	b = uint8(avgB)
	brightness = 0.299*avgR + 0.587*avgG + 0.114*avgB
	return
}
