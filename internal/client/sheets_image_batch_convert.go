package client

import (
	"fmt"
	"image"
	"image/png"
	"io"
	"os"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
	"golang.org/x/image/webp"
)

// normalizeSheetImageFile 将服务端无法稳定接受的格式转为真实 PNG，始终保留原文件。
func normalizeSheetImageFile(source, ext string, maxBytes int64) (string, string, error) {
	var decode func(io.Reader) (image.Image, error)
	switch ext {
	case ".bmp":
		decode = bmp.Decode
	case ".tiff":
		decode = tiff.Decode
	case ".webp":
		decode = webp.Decode
	default:
		return source, ext, nil
	}
	input, err := os.Open(source)
	if err != nil {
		return "", "", fmt.Errorf("读取待转换图片失败: %w", err)
	}
	img, err := decode(input)
	_ = input.Close()
	if err != nil {
		return "", "", fmt.Errorf("解析 %s 图片失败: %w", ext, err)
	}
	output, err := os.CreateTemp("", "feishu-sheet-img-*.png")
	if err != nil {
		return "", "", fmt.Errorf("创建转换图片失败: %w", err)
	}
	name := output.Name()
	writer := &sheetImageLimitedWriter{writer: output, remaining: maxBytes}
	encodeErr := png.Encode(writer, img)
	closeErr := output.Close()
	if encodeErr != nil || closeErr != nil {
		_ = os.Remove(name)
		if encodeErr != nil {
			return "", "", fmt.Errorf("转换 PNG 失败（转换后图片上限 %d 字节）: %w", maxBytes, encodeErr)
		}
		return "", "", fmt.Errorf("保存转换图片失败: %w", closeErr)
	}
	return name, ".png", nil
}

// 限制转换产物的实际大小，避免压缩率不同使输出超过用户配置的单图上限。
type sheetImageLimitedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *sheetImageLimitedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, fmt.Errorf("转换后的图片大小超过上限")
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}
