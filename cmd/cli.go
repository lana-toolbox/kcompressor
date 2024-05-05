package cmd

import (
	"fmt"
	"github.com/phsym/console-slog"
	"github.com/spf13/cobra"
	"image"
	"image/color"
	_ "image/jpeg"
	"image/png"
	"kcomp/internal/kmeans"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func Init() *slog.LevelVar {
	level := &slog.LevelVar{}
	logger := slog.New(
		console.NewHandler(os.Stderr, &console.HandlerOptions{
			Level:      level,
			TimeFormat: time.Kitchen,
		}))
	slog.SetDefault(logger)
	cobra.EnableCommandSorting = false
	return level
}

type CLI struct {
	command *cobra.Command
}

// NewCLI create new CLI instance and setup application config.
func NewCLI() *CLI {
	level := Init()
	f := flags{
		Colors:       12,
		Output:       "kcompressed",
		Round:        100,
		Concurrency:  8,
		DistanceAlgo: "EuclideanDistance",
	}

	command := cobra.Command{
		Use:   "kcomp [file]",
		Short: "Reduce number of colors used in image",
		Args:  cobra.ExactArgs(1),
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			debug, err := cmd.PersistentFlags().GetBool("debug")
			if err != nil {
				return err
			}
			if debug {
				level.Set(slog.LevelDebug)
			}
			return nil
		},
		Run: func(cmd *cobra.Command, args []string) {
			if o, err := cmd.Flags().GetBool("out-current-dir"); err == nil && o {
				f.Output = "."
			}

			if _, err := os.Stat(f.Output); err != nil {
				err := os.Mkdir(f.Output, os.ModePerm)
				if err != nil {
					slog.Info("Error creating output directory", slog.Any("dir", f.Output))
					return
				}
			}

			if s, err := cmd.Flags().GetInt("series"); err == nil && s > 1 {
				step := f.Colors / s
				start := 1
				if step <= 1 {
					start = 2
					step = 1
					s = f.Colors
				}

				for i := start; i < s; i++ {
					sf := f
					sf.Colors = step * i
					process(args[0], sf)
				}
			}
			process(args[0], f)
			slog.Info("Processing completed.")
		},
	}

	command.Flags().IntVarP(&f.Colors, "colors", "n", f.Colors, "Number of colors to use")
	command.Flags().StringVarP(&f.Output, "out", "o", f.Output, "Output directory name")
	command.Flags().BoolP("out-current-dir", "O", false, "Output on current directory, same as --out=.")
	command.Flags().IntP("series", "s", 1, "Number of image to generate, series of output with increasing number of colors up util reached --colors parameter")
	command.Flags().BoolVarP(&f.Overwrite, "overwrite", "w", f.Overwrite, "Overwrite output if exists")
	command.Flags().IntVarP(&f.Round, "round", "i", f.Round, "Maximum number of round before stop adjusting (number of kmeans iterations)")
	command.Flags().IntVarP(&f.Concurrency, "concurrency", "t", f.Concurrency, "Maximum number image process at a time")
	command.Flags().StringVar(&f.DistanceAlgo, "dalgo", f.DistanceAlgo, "Distance algo for kmeans [EuclideanDistance,EuclideanDistanceSquared,Squared]")
	command.PersistentFlags().Bool("debug", false, "Enable debug mode")
	return &CLI{&command}
}

func process(path string, f flags) {
	ch := scan(path)
	con := make(chan struct{}, f.Concurrency)
	for i := 0; i < f.Concurrency; i++ {
		con <- struct{}{}
		go func() {
			defer func() {
				<-con
			}()
			for img := range ch {
				handleImg(img, f)
			}
		}()
	}
	for i := 0; i < f.Concurrency; i++ {
		con <- struct{}{}
	}
}

func handleImg(img DecodedImage, f flags) {
	slog.Info("Processing",
		slog.Any("cp", f.Colors),
		slog.Any("round", f.Round),
		slog.String("img", filepath.Base(img.Path)),
		slog.String("dimension", fmt.Sprintf("%dx%d", img.Width, img.Height)),
		slog.String("format", img.Type),
	)
	outfile := filepath.Join(f.Output, strings.TrimSuffix(filepath.Base(img.Path), filepath.Ext(img.Path))+"."+strconv.Itoa(f.Round)+"cp"+strconv.Itoa(f.Colors)+".png")
	if _, err := os.Stat(outfile); err == nil {
		slog.Info("File existed",
			slog.Any("path", outfile),
			slog.Bool("override", f.Overwrite),
		)
		if !f.Overwrite {
			return
		}
	}

	now := time.Now()
	d := make([][]float64, 0, img.Width*img.Height)
	for y := 0; y < img.Height; y++ {
		for x := 0; x < img.Width; x++ {
			r, g, b, a := img.At(x, y).RGBA()
			if img.Type == "jpeg" {
				d = append(d, []float64{float64(r >> 8), float64(g >> 8), float64(b >> 8)})
			} else {
				d = append(d, []float64{float64(r >> 8), float64(g >> 8), float64(b >> 8), float64(a >> 8)})
			}
		}
	}

	algo := kmeans.EuclideanDistance
	switch f.DistanceAlgo {
	case "Squared":
		fallthrough
	case "EuclideanDistanceSquared":
		algo = kmeans.EuclideanDistanceSquared
	}

	slog.Debug("Start partitioning",
		slog.Int("cp", f.Colors),
		slog.String("img", filepath.Base(img.Path)),
		slog.Int("round", f.Round),
	)
	c := kmeans.New(f.Round, f.Colors, algo)
	c.Learn(d)
	rbga := image.NewRGBA(image.Rectangle{Min: image.Point{}, Max: image.Point{X: img.Width, Y: img.Height}})
	for index, number := range c.Guesses() {
		cluster := c.Cluster(number)
		y := index / img.Width
		x := index % img.Width
		if img.Type == "jpeg" {
			rbga.Set(x, y, color.RGBA{
				R: round(cluster[0]),
				G: round(cluster[1]),
				B: round(cluster[2]),
				A: 255,
			})
		} else {
			rbga.SetRGBA(x, y, color.RGBA{
				R: round(cluster[0]),
				G: round(cluster[1]),
				B: round(cluster[2]),
				A: round(cluster[3]),
			})
		}
	}
	o, err := os.Create(outfile)
	if err == nil {
		err = png.Encode(o, rbga)
	}
	if err != nil {
		slog.Error("Error writing image", slog.String("out", outfile), slog.Any("err", err))
		return
	}
	slog.Info("Compress completed", slog.String("out", outfile), slog.Duration("took", time.Since(now)))
}

func round(f float64) uint8 {
	return uint8(math.Round(f))
}

type flags struct {
	Colors       int
	Output       string
	Round        int
	Overwrite    bool
	Concurrency  int
	DistanceAlgo string
}

func scan(dir string) <-chan DecodedImage {
	ch := make(chan DecodedImage, 1)
	info, err := os.Stat(dir)
	if err != nil {
		slog.Error("Err scanning file(s)", slog.String("path", dir), slog.Any("err", err))
		close(ch)
		return ch
	}

	go func() {
		defer close(ch)
		if !info.IsDir() {
			img, err := decode(dir)
			if err != nil {
				slog.Error("Err decoding image", slog.String("path", dir), slog.Any("err", err))
				return
			}
			ch <- img
			return
		}

		files, err := os.ReadDir(".")
		if err != nil {
			slog.Error("Err scanning dir", slog.String("path", dir), slog.Any("err", err))
			return
		}

		for _, file := range files {
			if file.IsDir() {
				continue
			}
			path := filepath.Join(dir, file.Name())
			img, err := decode(path)
			if err != nil {
				slog.Error("Not a image", slog.String("path", path), slog.Any("err", err))
				continue
			}
			ch <- img
		}
	}()

	return ch
}

func decode(path string) (DecodedImage, error) {
	img := DecodedImage{
		Path: path,
	}
	f, err := os.Open(path)
	if err != nil {
		return img, err
	}
	defer f.Close()

	config, _, err := image.DecodeConfig(f)
	if err != nil {
		return img, err
	}
	img.Config = config

	_, err = f.Seek(0, 0)
	if err != nil {
		panic(err)
	}
	slog.Debug("Decoding image", slog.String("path", path), slog.String("dimension", fmt.Sprintf("%dx%d", img.Config.Width, img.Config.Height)))
	imageData, imageType, err := image.Decode(f)
	if err != nil {
		return img, err
	}
	img.Type = imageType
	img.Image = imageData

	return img, nil
}

type DecodedImage struct {
	image.Image
	image.Config
	Type string
	Path string
}

func (cli *CLI) Execute() {
	if err := cli.command.Execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
	}
}
