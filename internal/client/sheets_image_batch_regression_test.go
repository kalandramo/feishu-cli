package client

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/image/bmp"
	"golang.org/x/image/tiff"
)

func sheetImageFixture(t *testing.T, format string) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	var err error
	switch format {
	case "png":
		err = png.Encode(&buf, img)
	case "bmp":
		err = bmp.Encode(&buf, img)
	case "tiff":
		err = tiff.Encode(&buf, img, nil)
	case "webp":
		// 2×2 黑色无损 WebP，测试 fixture 不依赖运行时图像工具。
		data, decodeErr := base64.StdEncoding.DecodeString("UklGRhoAAABXRUJQVlA4TA4AAAAvAUAAAAcQEf0PRET/Aw==")
		if decodeErr != nil {
			t.Fatal(decodeErr)
		}
		return data
	default:
		t.Fatalf("未知测试图片格式 %s", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sheetImageReadback(w http.ResponseWriter, ranges []string) {
	values := make([]*CellRangeV3, 0, len(ranges))
	for _, cell := range ranges {
		values = append(values, &CellRangeV3{Range: cell, Values: [][][]*CellElement{{{
			{Type: "image", Image: &ImageElement{ImageToken: "image-" + extractCellCoordinate(cell)}},
		}}}})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": map[string]any{"value_ranges": values}})
}

func TestBatchWriteSheetImagesSerialWritesAndPartialFailure(t *testing.T) {
	dir := t.TempDir()
	formats := []string{"png", "bmp", "tiff", "webp"}
	items := make([]BatchWriteSheetImageItem, 0, 14)
	wantNames := make(map[string]string)
	for i := 0; i < 13; i++ {
		format := formats[i%len(formats)]
		// 无后缀本地路径，以及显式无后缀/错误后缀名称，均应按实际内容补正。
		p := filepath.Join(dir, fmt.Sprintf("image-%d", i))
		if err := os.WriteFile(p, sheetImageFixture(t, format), 0o600); err != nil {
			t.Fatal(err)
		}
		name := fmt.Sprintf("product-%d", i)
		if i%2 == 0 {
			name += ".php"
		}
		cell := fmt.Sprintf("s1!A%d:A%d", i+1, i+1)
		items = append(items, BatchWriteSheetImageItem{Cell: cell, Path: p, Name: name})
		wantNames[cell] = fmt.Sprintf("product-%d.png", i)
	}
	items[0].Name = ""
	wantNames[items[0].Cell] = "image-0.png"
	items = append(items, BatchWriteSheetImageItem{Cell: "s1!A14:A14", Path: filepath.Join(dir, "missing.png")})

	var active, overlapping atomic.Int32
	var writes atomic.Int32
	var readSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "Bearer u-test" {
			t.Errorf("未使用指定 User Token")
		}
		if strings.HasSuffix(r.URL.Path, "/values_image") {
			writes.Add(1)
			if active.Add(1) > 1 {
				overlapping.Add(1)
			}
			defer active.Add(-1)
			var body struct {
				Range, Name string
				Image       []int
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body.Name != wantNames[body.Range] {
				t.Errorf("%s: name = %q, want %q", body.Range, body.Name, wantNames[body.Range])
			}
			imageBytes := make([]byte, len(body.Image))
			for i, b := range body.Image {
				imageBytes[i] = byte(b)
			}
			if _, err := png.Decode(bytes.NewReader(imageBytes)); err != nil {
				t.Errorf("%s: 应上传真实 PNG 字节: %v", body.Range, err)
			}
			// 模拟有处理时长的写接口，使错误的并发实现稳定产生重叠。
			time.Sleep(15 * time.Millisecond)
			if body.Range == "s1!A13:A13" {
				_, _ = io.WriteString(w, `{"code":90218,"msg":"LockedCell"}`)
				return
			}
			_, _ = io.WriteString(w, `{"code":0}`)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/values/batch_get") {
			t.Errorf("意外请求: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body struct{ Ranges []string }
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		readSizes = append(readSizes, len(body.Ranges))
		for _, cell := range body.Ranges {
			if cell == "s1!A13:A13" || cell == "s1!A14:A14" {
				t.Errorf("不应回读未写入的单元格 %s", cell)
			}
		}
		sheetImageReadback(w, body.Ranges)
	}))
	defer srv.Close()
	setupTestConfig(t, srv.URL)

	result, err := BatchWriteSheetImages(context.Background(), "sht_test", "s1", items, BatchWriteSheetImageOptions{Workers: 4}, "u-test")
	if err == nil || result.Verified || result.Written != 12 || result.Failed != 2 {
		t.Fatalf("部分失败结果错误: %+v, err=%v", result, err)
	}
	if overlapping.Load() != 0 || writes.Load() != 13 {
		t.Fatalf("写入重叠=%d，调用数=%d", overlapping.Load(), writes.Load())
	}
	if len(readSizes) != 2 || readSizes[0] != 10 || readSizes[1] != 2 {
		t.Fatalf("回读分批错误: %v", readSizes)
	}
	for i, outcome := range result.Outcomes[:12] {
		if outcome.Status != "verified" || outcome.ImageToken != fmt.Sprintf("image-A%d", i+1) {
			t.Errorf("第 %d 项结果未按 manifest 顺序对应回读: %+v", i+1, outcome)
		}
	}
}

func TestSheetImageDownloadsRemainConcurrentAndWaitingCancels(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	data := sheetImageFixture(t, "png")
	downloadStarted := make(chan struct{}, 2)
	releaseDownloads := make(chan struct{})
	httpClient := &http.Client{Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
		downloadStarted <- struct{}{}
		<-releaseDownloads
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Header: make(http.Header)}, nil
	})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writeSlot := make(chan struct{}, 1)
	writeSlot <- struct{}{} // 模拟前一个图片写入仍在执行。
	var wg sync.WaitGroup
	results := make([]BatchWriteSheetImageOutcome, 2)
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index] = writeSingleSheetImage(ctx, httpClient, "sht_test", BatchWriteSheetImageItem{Cell: "s1!A1:A1", URL: "https://10.0.0.1/image"}, 1024, true, "u-test", writeSlot)
		}(i)
	}
	for range results {
		select {
		case <-downloadStarted:
		case <-time.After(time.Second):
			close(releaseDownloads)
			cancel()
			wg.Wait()
			t.Fatal("等待写入时不应阻止其他 worker 下载")
		}
	}
	close(releaseDownloads)
	deadline := time.Now().Add(time.Second)
	for {
		files, err := os.ReadDir(tmp)
		if err != nil {
			t.Fatal(err)
		}
		if len(files) == 2 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			wg.Wait()
			t.Fatal("下载后应仅保留每个 worker 的待写文件")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("等待写入未响应取消")
	}
	for _, result := range results {
		if result.Status != "failed" || !strings.Contains(result.Error, "中断") {
			t.Errorf("取消结果异常: %+v", result)
		}
	}
	files, err := os.ReadDir(tmp)
	if err != nil || len(files) != 0 {
		t.Fatalf("取消后临时文件未清理: %v, %v", files, err)
	}
}

func TestSheetImageFormatsAndNames(t *testing.T) {
	ftyp := func(major, compatible string) []byte {
		b := make([]byte, 20)
		binary.BigEndian.PutUint32(b, uint32(len(b)))
		copy(b[4:], "ftyp"+major)
		copy(b[16:], compatible)
		return b
	}
	cases := []struct {
		name string
		data []byte
		ext  string
	}{
		{"PNG", sheetImageFixture(t, "png"), ".png"},
		{"BMP", sheetImageFixture(t, "bmp"), ".bmp"},
		{"TIFF", sheetImageFixture(t, "tiff"), ".tiff"},
		{"有效 WebP", sheetImageFixture(t, "webp"), ".webp"},
		{"JPEG/JFIF", []byte("\xff\xd8\xff\xe0\x00\x10JFIF\x00"), ".jpg"},
		{"JPEG/EXIF", []byte("\xff\xd8\xff\xe1\x00\x10Exif\x00\x00"), ".jpg"},
		{"GIF", []byte("GIF89a"), ".gif"},
		{"WebP", []byte("RIFF\x10\x00\x00\x00WEBPVP8 "), ".webp"},
		{"TIFF big endian", []byte("MM\x00*\x00\x00\x00\x08"), ".tiff"},
		{"BPG", []byte("BPG\xfb\x00\x00\x01\x01\x00"), ".bpg"},
		{"HEIC major brand", ftyp("heic", "mif1"), ".heic"},
		{"HEIC compatible brand", ftyp("mif1", "heic"), ".heic"},
		{"AVIF 非支持格式", ftyp("avif", "mif1"), ""},
		{"普通文本", []byte("not an image.png"), ""},
		{"伪造后部 brand", append([]byte("not an image file"), ftyp("heic", "mif1")...), ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ext, ok := detectSheetImageExtension(tc.data)
			if ext != tc.ext || ok != (tc.ext != "") {
				t.Errorf("识别结果=(%q,%t), want %q", ext, ok, tc.ext)
			}
		})
	}
	for _, tc := range []struct{ name, ext, want string }{
		{"download", ".png", "download.png"},
		{"proxy.php", ".png", "proxy.png"},
		{"产品.jpg", ".bmp", "产品.bmp"},
		{"photo.JPEG", ".jpg", "photo.JPEG"},
		{"photo.jfif", ".jpg", "photo.jfif"},
		{"photo.exif", ".jpg", "photo.exif"},
		{"scan.tiff", ".tiff", "scan.tiff"},
		{"", ".heic", "image.heic"},
	} {
		if got := normalizeSheetImageName(tc.name, tc.ext); got != tc.want {
			t.Errorf("name=%q ext=%q: got %q, want %q", tc.name, tc.ext, got, tc.want)
		}
	}
}

func TestDownloadSheetImageNormalizesURLName(t *testing.T) {
	data := sheetImageFixture(t, "png")
	httpClient := &http.Client{Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Header: make(http.Header)}, nil
	})}
	for _, tc := range []struct{ path, want string }{{"/image?id=123", "image.png"}, {"/proxy.php", "proxy.png"}, {"/photo.jpg", "photo.png"}, {"/", "image.png"}} {
		p, name, err := downloadSheetImage(context.Background(), httpClient, "https://10.0.0.1"+tc.path, 1024, true)
		if err != nil {
			t.Fatal(err)
		}
		_ = os.Remove(p)
		if name != tc.want {
			t.Errorf("%s: name=%q, want %q", tc.path, name, tc.want)
		}
	}
}

func TestBatchWriteSheetImagesRetriesSheetErrors(t *testing.T) {
	for _, code := range []int{90217, 90235} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "image.png")
			if err := os.WriteFile(p, sheetImageFixture(t, "png"), 0o600); err != nil {
				t.Fatal(err)
			}
			var attempts atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/values_image") {
					if attempts.Add(1) == 1 {
						_, _ = fmt.Fprintf(w, `{"code":%d,"msg":"Retry later"}`, code)
					} else {
						_, _ = io.WriteString(w, `{"code":0}`)
					}
					return
				}
				sheetImageReadback(w, []string{"s1!A1:A1"})
			}))
			defer srv.Close()
			setupTestConfig(t, srv.URL)
			result, err := BatchWriteSheetImages(context.Background(), "sht_test", "s1", []BatchWriteSheetImageItem{{Cell: "s1!A1:A1", Path: p}}, BatchWriteSheetImageOptions{}, "u-test")
			if err != nil || !result.Verified || attempts.Load() != 2 {
				t.Fatalf("重试结果错误: %+v, err=%v, attempts=%d", result, err, attempts.Load())
			}
		})
	}
}

func TestNormalizeSheetImageFilePreservesSourceAndBoundsOutput(t *testing.T) {
	tmp := t.TempDir()
	sources := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	for _, format := range []string{"bmp", "tiff", "webp"} {
		t.Run(format, func(t *testing.T) {
			data := sheetImageFixture(t, format)
			source := filepath.Join(sources, "source."+format)
			if err := os.WriteFile(source, data, 0o600); err != nil {
				t.Fatal(err)
			}
			p, ext, err := normalizeSheetImageFile(source, "."+format, 1024)
			if err != nil {
				t.Fatal(err)
			}
			converted, err := os.ReadFile(p)
			_ = os.Remove(p)
			if err != nil || p == source || ext != ".png" {
				t.Fatalf("转换产物错误: %s, %s, %v", p, ext, err)
			}
			img, err := png.Decode(bytes.NewReader(converted))
			if err != nil || img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
				t.Fatalf("PNG 内容错误: %v", err)
			}
			original, err := os.ReadFile(source)
			if err != nil || !bytes.Equal(original, data) {
				t.Fatal("转换不得修改输入文件")
			}
			if _, _, err := normalizeSheetImageFile(source, "."+format, 10); err == nil {
				t.Fatal("转换后超出大小上限应报错")
			}
			if err := os.WriteFile(source, []byte("broken image"), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := normalizeSheetImageFile(source, "."+format, 1024); err == nil {
				t.Fatal("损坏的图片不得仅更换后缀上传")
			}
			files, err := os.ReadDir(tmp)
			if err != nil || len(files) != 0 {
				t.Fatalf("转换失败未清理临时文件: %v, %v", files, err)
			}
		})
	}
}
