package orbis

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/gif"  // cover art is occasionally a GIF
	_ "image/jpeg" // covers are downloaded as JPEG
	"image/png"
	"math"
	"os"
)

// ResizePNG loads an image, scales it to exactly width by height ignoring the
// original aspect ratio, and returns it encoded as a PNG. This replaces the
// ImageMagick calls the Windows toolchain used for sce_sys artwork.
func ResizePNG(source string, width, height int) ([]byte, error) {
	if width <= 0 || height <= 0 {
		return nil, fmt.Errorf("invalid target size %dx%d", width, height)
	}
	file, err := os.Open(source)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	decoded, _, err := image.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", source, err)
	}
	resized := resizeImage(decoded, width, height)
	var out bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := encoder.Encode(&out, resized); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// resizeImage scales an image with a separable triangle filter, which averages
// neighbouring pixels when shrinking and interpolates when enlarging.
func resizeImage(source image.Image, width, height int) *image.NRGBA {
	bounds := source.Bounds()
	src := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(src, src.Bounds(), source, bounds.Min, draw.Src)
	horizontal := resampleAxis(src, width, true)
	return resampleAxis(horizontal, height, false)
}

// resampleAxis scales one axis of the image to the given size.
func resampleAxis(src *image.NRGBA, size int, horizontal bool) *image.NRGBA {
	sourceWidth, sourceHeight := src.Bounds().Dx(), src.Bounds().Dy()
	sourceSize := sourceHeight
	if horizontal {
		sourceSize = sourceWidth
	}
	width, height := sourceWidth, sourceHeight
	if horizontal {
		width = size
	} else {
		height = size
	}
	dst := image.NewNRGBA(image.Rect(0, 0, width, height))
	if sourceSize == 0 || size == 0 {
		return dst
	}
	scale := float64(size) / float64(sourceSize)
	radius := 1.0
	if scale < 1 {
		radius = 1 / scale
	}
	for out := 0; out < size; out++ {
		center := (float64(out)+0.5)/scale - 0.5
		start := int(math.Ceil(center - radius))
		end := int(math.Floor(center + radius))
		weights := make([]float64, 0, end-start+1)
		indexes := make([]int, 0, end-start+1)
		total := 0.0
		for i := start; i <= end; i++ {
			clamped := i
			if clamped < 0 {
				clamped = 0
			}
			if clamped >= sourceSize {
				clamped = sourceSize - 1
			}
			weight := 1 - math.Abs(float64(i)-center)/radius
			if weight <= 0 {
				continue
			}
			indexes = append(indexes, clamped)
			weights = append(weights, weight)
			total += weight
		}
		if total == 0 {
			continue
		}
		lines := height
		if horizontal {
			lines = sourceHeight
		} else {
			lines = sourceWidth
		}
		for line := 0; line < lines; line++ {
			var r, g, b, a float64
			for i, index := range indexes {
				var pixel []uint8
				if horizontal {
					offset := src.PixOffset(index, line)
					pixel = src.Pix[offset : offset+4]
				} else {
					offset := src.PixOffset(line, index)
					pixel = src.Pix[offset : offset+4]
				}
				weight := weights[i]
				alpha := float64(pixel[3]) / 255
				r += float64(pixel[0]) * weight * alpha
				g += float64(pixel[1]) * weight * alpha
				b += float64(pixel[2]) * weight * alpha
				a += float64(pixel[3]) * weight
			}
			alpha := a / total
			var offset int
			if horizontal {
				offset = dst.PixOffset(out, line)
			} else {
				offset = dst.PixOffset(line, out)
			}
			if alpha <= 0 {
				dst.Pix[offset], dst.Pix[offset+1], dst.Pix[offset+2], dst.Pix[offset+3] = 0, 0, 0, 0
				continue
			}
			straight := total * (alpha / 255)
			dst.Pix[offset] = clamp8(r / straight)
			dst.Pix[offset+1] = clamp8(g / straight)
			dst.Pix[offset+2] = clamp8(b / straight)
			dst.Pix[offset+3] = clamp8(alpha)
		}
	}
	return dst
}

func clamp8(value float64) uint8 {
	if value <= 0 {
		return 0
	}
	if value >= 255 {
		return 255
	}
	return uint8(value + 0.5)
}
