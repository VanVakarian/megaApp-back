package food

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"megaapp-back/internal/httpx/legacy"

	"github.com/chai2010/webp"
	"github.com/disintegration/imaging"
	"golang.org/x/image/vector"
	_ "golang.org/x/image/webp"
)

type GeneratedImage struct {
	Data     []byte
	Format   string
	Model    string
	Provider string
}

type ImageGenerator interface {
	GenerateFoodImage(ctx context.Context, prompt string) (GeneratedImage, error)
}

type ImageRateLimitChecker interface {
	CheckRateLimits(ctx context.Context) (map[string]any, error)
}

type ImageGenerationRequester interface {
	RequestProductImageGeneration(catalogueID int64, productName string, description string)
}

type ImageVersionProvider interface {
	ImageVersion(catalogueID int64) *int64
}

type ImageGenerationResult struct {
	CatalogueID       int64  `json:"catalogueId"`
	ThumbnailFilename string `json:"thumbnailFilename"`
	OriginalFilename  string `json:"originalFilename"`
	MediumFilename    string `json:"mediumFilename"`
	LargeFilename     string `json:"largeFilename"`
	SquircleFilename  string `json:"squircleFilename"`
	CornerFilename    string `json:"cornerFilename"`
}

type ImageRebuildResult struct {
	CatalogueID      int64  `json:"catalogueId"`
	Version          int64  `json:"version"`
	PreviousVersion  int64  `json:"previousVersion"`
	OriginalFilename string `json:"originalFilename"`
	ThumbFilename    string `json:"thumbFilename"`
	MediumFilename   string `json:"mediumFilename"`
	LargeFilename    string `json:"largeFilename"`
	SquircleFilename string `json:"squircleFilename"`
	CornerFilename   string `json:"cornerFilename"`
}

type queueTask struct {
	catalogueID int64
	productName string
	description string
	attempts    int
	availableAt time.Time
}

type ImagePipeline struct {
	store        *ImageStore
	generator    ImageGenerator
	realtime     RealtimePublisher
	logger       *slog.Logger
	mu           sync.Mutex
	queue        map[int64]*queueTask
	queueOrder   []int64
	closed       bool
	notify       chan struct{}
	stop         chan struct{}
	maxQueueSize int
	maxAttempts  int
	rateLimit    time.Duration
}

func NewImagePipeline(store *ImageStore, generator ImageGenerator, realtime RealtimePublisher, maxAttempts int, logger *slog.Logger) *ImagePipeline {
	pipeline := &ImagePipeline{
		store:        store,
		generator:    generator,
		realtime:     realtime,
		logger:       logger,
		queue:        map[int64]*queueTask{},
		notify:       make(chan struct{}, 1),
		stop:         make(chan struct{}),
		maxQueueSize: 24,
		maxAttempts:  maxAttempts,
		rateLimit:    time.Second,
	}
	go pipeline.run()
	return pipeline
}

func (p *ImagePipeline) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	close(p.stop)
	p.mu.Unlock()
	return nil
}

func (p *ImagePipeline) RequestProductImageGeneration(catalogueID int64, productName string, description string) {
	if p == nil {
		return
	}
	if p.generator == nil {
		p.logger.Warn("image generation skipped: generator not configured", "catalogueId", catalogueID)
		return
	}
	if p.store.ImageVersion(catalogueID) != nil {
		p.logger.Debug("image generation skipped: image already exists", "catalogueId", catalogueID)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	if p.queue[catalogueID] != nil {
		p.logger.Debug("image generation skipped: already queued", "catalogueId", catalogueID)
		return
	}
	if len(p.queue) >= p.maxQueueSize {
		p.logger.Warn("image generation skipped: queue is full", "catalogueId", catalogueID, "queueSize", len(p.queue))
		return
	}
	p.queue[catalogueID] = &queueTask{
		catalogueID: catalogueID,
		productName: productName,
		description: description,
		availableAt: time.Now(),
	}
	p.queueOrder = append(p.queueOrder, catalogueID)
	p.logger.Debug("image generation enqueued", "catalogueId", catalogueID, "queueSize", len(p.queue))
	select {
	case p.notify <- struct{}{}:
	default:
	}
}

func (p *ImagePipeline) run() {
	for {
		task, waitDuration, ok := p.nextTask()
		if !ok {
			select {
			case <-p.stop:
				return
			case <-p.notify:
			}
			continue
		}
		if waitDuration > 0 {
			timer := time.NewTimer(waitDuration)
			select {
			case <-p.stop:
				timer.Stop()
				return
			case <-p.notify:
				timer.Stop()
				continue
			case <-timer.C:
			}
		}
		p.processTask(task)
		select {
		case <-p.stop:
			return
		case <-time.After(p.rateLimit):
		}
	}
}

func (p *ImagePipeline) nextTask() (*queueTask, time.Duration, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || len(p.queueOrder) == 0 {
		return nil, 0, false
	}
	now := time.Now()
	var candidate *queueTask
	var candidateWait time.Duration
	for _, id := range p.queueOrder {
		task := p.queue[id]
		if task == nil {
			continue
		}
		wait := task.availableAt.Sub(now)
		if candidate == nil || wait < candidateWait {
			candidate = task
			candidateWait = wait
		}
	}
	if candidate == nil {
		return nil, 0, false
	}
	if candidateWait < 0 {
		candidateWait = 0
	}
	return candidate, candidateWait, true
}

func (p *ImagePipeline) processTask(task *queueTask) {
	p.logger.Debug("image generation task started", "catalogueId", task.catalogueID, "productName", task.productName, "attempt", task.attempts+1, "maxAttempts", p.maxAttempts)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err := p.GenerateProductImage(ctx, task.catalogueID, task.productName, task.description)
	if err == nil {
		p.logger.Debug("image generation task succeeded", "catalogueId", task.catalogueID, "attempt", task.attempts+1)
		p.removeTask(task.catalogueID)
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	current := p.queue[task.catalogueID]
	if current == nil {
		return
	}
	current.attempts++
	if current.attempts >= p.maxAttempts {
		p.logger.Error("image generation failed permanently", "catalogueId", task.catalogueID, "productName", task.productName, "attempts", current.attempts, "error", err)
		delete(p.queue, task.catalogueID)
		p.queueOrder = removeQueueID(p.queueOrder, task.catalogueID)
		return
	}
	p.logger.Warn("image generation attempt failed, retrying", "catalogueId", task.catalogueID, "productName", task.productName, "attempt", current.attempts, "maxAttempts", p.maxAttempts, "error", err)
	current.availableAt = time.Now().Add(time.Duration(1<<max(0, current.attempts-1)) * time.Second)
}

func (p *ImagePipeline) removeTask(catalogueID int64) {
	p.mu.Lock()
	delete(p.queue, catalogueID)
	p.queueOrder = removeQueueID(p.queueOrder, catalogueID)
	p.mu.Unlock()
}

func removeQueueID(ids []int64, target int64) []int64 {
	result := ids[:0]
	for _, id := range ids {
		if id != target {
			result = append(result, id)
		}
	}
	return result
}

func (p *ImagePipeline) GenerateProductImage(ctx context.Context, catalogueID int64, productName string, description string) (*ImageGenerationResult, error) {
	if p == nil || p.generator == nil {
		return nil, legacy.NewError(legacy.ErrorKindValidation, "Image generation is disabled")
	}
	prompt := buildFoodImagePrompt(productName, description)
	p.logger.Debug("image prompt built", "catalogueId", catalogueID, "promptLength", len(prompt))
	generated, err := p.generator.GenerateFoodImage(ctx, prompt)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindExternal, "Failed to generate image", err)
	}
	version := int64(1)
	if current := p.store.ImageVersion(catalogueID); current != nil {
		version = *current + 1
	}
	p.logger.Debug("image generated, persisting variants", "catalogueId", catalogueID, "version", version, "format", generated.Format, "bytes", len(generated.Data))
	originalFilename, result, err := p.persistGeneratedImage(catalogueID, version, generated)
	if err != nil {
		return nil, err
	}
	result.OriginalFilename = originalFilename
	p.store.SetImageVersion(catalogueID, version)
	p.realtime.PublishCatalogueImageGenerated(catalogueID, version, "")
	p.logger.Debug("image generation completed", "catalogueId", catalogueID, "version", version)
	return result, nil
}

func (p *ImagePipeline) RebuildImageVariants(ctx context.Context, catalogueID int64) (*ImageRebuildResult, error) {
	if p == nil {
		return nil, legacy.NewError(legacy.ErrorKindValidation, "Image pipeline is not configured")
	}
	oldName, originalPath, previousVersion, err := p.store.FindLatestOriginal(catalogueID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, legacy.NewError(legacy.ErrorKindNotFound, fmt.Sprintf("Original image not found for product %d", catalogueID))
		}
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to locate original image", err)
	}
	data, err := os.ReadFile(originalPath)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to read original image", err)
	}
	decoded, err := decodeImage(data)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to decode original image", err)
	}
	newVersion := previousVersion + 1
	if err := p.store.DeleteOldVersions(catalogueID, newVersion); err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to clear old image versions", err)
	}
	newOriginalName, err := p.store.RenameOriginal(oldName, catalogueID, newVersion)
	if err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to rotate original image version", err)
	}
	files, err := p.writeVariants(catalogueID, newVersion, decoded)
	if err != nil {
		return nil, err
	}
	p.store.SetImageVersion(catalogueID, newVersion)
	p.realtime.PublishCatalogueImageGenerated(catalogueID, newVersion, "")
	return &ImageRebuildResult{
		CatalogueID:      catalogueID,
		Version:          newVersion,
		PreviousVersion:  previousVersion,
		OriginalFilename: newOriginalName,
		ThumbFilename:    files.ThumbnailFilename,
		MediumFilename:   files.MediumFilename,
		LargeFilename:    files.LargeFilename,
		SquircleFilename: files.SquircleFilename,
		CornerFilename:   files.CornerFilename,
	}, nil
}

func (p *ImagePipeline) persistGeneratedImage(catalogueID int64, version int64, generated GeneratedImage) (string, *ImageGenerationResult, error) {
	decoded, err := decodeImage(generated.Data)
	if err != nil {
		return "", nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to decode generated image", err)
	}
	bounds := decoded.Bounds()
	p.logger.Debug("generated image decoded", "catalogueId", catalogueID, "width", bounds.Dx(), "height", bounds.Dy())
	if err := p.store.DeleteOldVersions(catalogueID, version); err != nil {
		return "", nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to clear old image versions", err)
	}
	originalFilename, err := p.store.SaveOriginal(catalogueID, version, generated.Format, generated.Data)
	if err != nil {
		return "", nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to save original image", err)
	}
	result, err := p.writeVariants(catalogueID, version, decoded)
	if err != nil {
		return "", nil, err
	}
	p.logger.Debug("image variants written", "catalogueId", catalogueID, "version", version)
	return originalFilename, result, nil
}

func (p *ImagePipeline) writeVariants(catalogueID int64, version int64, source image.Image) (*ImageGenerationResult, error) {
	result := &ImageGenerationResult{CatalogueID: catalogueID}
	thumb := imaging.Fill(source, 256, 256, imaging.Center, imaging.Lanczos)
	result.ThumbnailFilename = fmt.Sprintf("%d-thumb-v%d.webp", catalogueID, version)
	if err := encodeWebP(filepath.Join(p.store.FoodDir(), result.ThumbnailFilename), thumb, 80); err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to write thumbnail image", err)
	}

	medium := imaging.Fill(source, 512, 512, imaging.Center, imaging.Lanczos)
	result.MediumFilename = fmt.Sprintf("%d-medium-v%d.webp", catalogueID, version)
	if err := encodeWebP(filepath.Join(p.store.FoodDir(), result.MediumFilename), medium, 85); err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to write medium image", err)
	}

	large := imaging.Fill(source, 1024, 1024, imaging.Center, imaging.Lanczos)
	result.LargeFilename = fmt.Sprintf("%d-large-v%d.webp", catalogueID, version)
	if err := encodeWebP(filepath.Join(p.store.FoodDir(), result.LargeFilename), large, 90); err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to write large image", err)
	}

	squircleImage := buildSquircleVariant(source)
	result.SquircleFilename = fmt.Sprintf("%d-squircle-v%d.png", catalogueID, version)
	if err := encodePNG(filepath.Join(p.store.FoodDir(), result.SquircleFilename), squircleImage); err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to write squircle image", err)
	}

	cornerImage := buildCornerVariant(source)
	result.CornerFilename = fmt.Sprintf("%d-corner-v%d.png", catalogueID, version)
	if err := encodePNG(filepath.Join(p.store.FoodDir(), result.CornerFilename), cornerImage); err != nil {
		return nil, legacy.WrapError(legacy.ErrorKindInternal, "Failed to write corner image", err)
	}

	return result, nil
}

func decodeImage(data []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	return img, nil
}

func encodeWebP(path string, img image.Image, quality float32) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return webp.Encode(file, img, &webp.Options{Quality: quality})
}

func encodePNG(path string, img image.Image) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	encoder := png.Encoder{CompressionLevel: png.BestCompression}
	return encoder.Encode(file, img)
}

func buildSquircleVariant(source image.Image) image.Image {
	const squircleImageSize = 80
	const squircleImageZoomLevel = 1.5
	zoomedSize := int(math.Round(float64(squircleImageSize) * squircleImageZoomLevel))
	imageOffset := int(math.Round(float64(zoomedSize-squircleImageSize) / 2))
	zoomed := imaging.Fill(source, zoomedSize, zoomedSize, imaging.Center, imaging.Lanczos)
	cropped := imaging.Crop(zoomed, image.Rect(imageOffset, imageOffset, imageOffset+squircleImageSize, imageOffset+squircleImageSize))
	mask := buildSquircleMask(squircleImageSize)
	blurredMask := blurMask(mask, 0.8)
	return applyMask(cropped, blurredMask)
}

func buildCornerVariant(source image.Image) image.Image {
	const cornerImageSize = 370
	const cornerImageXOffsetPercent = -25.0
	offsetPixels := int(math.Abs(float64(jsRound(float64(cornerImageSize) * (cornerImageXOffsetPercent / 100)))))
	extendedWidth := cornerImageSize + offsetPixels
	resized := imaging.Fill(source, extendedWidth, cornerImageSize, imaging.Center, imaging.Lanczos)
	cropped := imaging.Crop(resized, image.Rect(offsetPixels, 0, offsetPixels+cornerImageSize, cornerImageSize))
	mask := buildCornerMask(cornerImageSize)
	blurredMask := blurMask(mask, 3)
	return applyMask(cropped, blurredMask)
}

func buildSquircleMask(size int) *image.Alpha {
	offset := float32(float64(size) * 0.06)
	corner := float32(float64(size) * 0.4)
	center := float32(float64(size) * (1 - 0.4))
	max := float32(size) - offset

	return rasterizeMask(size, size, func(r *vector.Rasterizer) {
		r.MoveTo(offset, corner)
		r.CubeTo(offset, offset, offset, offset, corner, offset)
		r.LineTo(center, offset)
		r.CubeTo(max, offset, max, offset, max, corner)
		r.LineTo(max, center)
		r.CubeTo(max, max, max, max, center, max)
		r.LineTo(corner, max)
		r.CubeTo(offset, max, offset, max, offset, center)
		r.ClosePath()
	})
}

func buildCornerMask(size int) *image.Alpha {
	offset := float32(float64(size) * 0.125)
	edgePoint := float32(float64(size) * 0.875)
	curvePoint := float32(float64(size) * 0.8125)

	return rasterizeMask(size, size, func(r *vector.Rasterizer) {
		r.MoveTo(-offset, -offset)
		r.LineTo(edgePoint, -offset)
		r.LineTo(edgePoint, 0)
		r.CubeTo(edgePoint, curvePoint, curvePoint, edgePoint, 0, edgePoint)
		r.LineTo(-offset, edgePoint)
		r.ClosePath()
	})
}

func rasterizeMask(width int, height int, build func(*vector.Rasterizer)) *image.Alpha {
	rasterizer := vector.NewRasterizer(width, height)
	build(rasterizer)
	mask := image.NewAlpha(image.Rect(0, 0, width, height))
	rasterizer.Draw(mask, mask.Bounds(), image.NewUniform(color.Alpha{A: 255}), image.Point{})
	return mask
}

func blurMask(mask *image.Alpha, sigma float64) *image.Alpha {
	if sigma <= 0 {
		return mask
	}
	blurInput := image.NewNRGBA(mask.Bounds())
	for y := mask.Bounds().Min.Y; y < mask.Bounds().Max.Y; y++ {
		for x := mask.Bounds().Min.X; x < mask.Bounds().Max.X; x++ {
			alpha := mask.AlphaAt(x, y).A
			blurInput.SetNRGBA(x, y, color.NRGBA{R: 255, G: 255, B: 255, A: alpha})
		}
	}
	blurred := imaging.Blur(blurInput, sigma)
	result := image.NewAlpha(mask.Bounds())
	for y := result.Bounds().Min.Y; y < result.Bounds().Max.Y; y++ {
		for x := result.Bounds().Min.X; x < result.Bounds().Max.X; x++ {
			result.SetAlpha(x, y, color.Alpha{A: color.AlphaModel.Convert(blurred.At(x, y)).(color.Alpha).A})
		}
	}
	return result
}

func applyMask(src image.Image, mask *image.Alpha) image.Image {
	bounds := src.Bounds()
	dst := image.NewNRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.DrawMask(dst, dst.Bounds(), imaging.Clone(src), bounds.Min, mask, image.Point{}, draw.Over)
	return dst
}

func jsRound(value float64) int {
	return int(math.Floor(value + 0.5))
}

func buildFoodImagePrompt(productName string, description string) string {
	name := normalizeSearchText(productName)
	text := strings.TrimSpace(description)
	if text == "" {
		text = name
	}
	return fmt.Sprintf("Абсолютно по центру изображения, идеально выровненный, крупный план продукта %s. %s. Объект находится точно по центру на светлой, слегка потертой и теплой на вид деревянной поверхности или на тактильно гладком, но не глянцевом мраморе с мелкими включениями. Мягкий, обволакивающий естественный свет из окна, подсвечивающий микро-текстуры продукта, малая глубина резкости, создающая бархатное боке, уютная и чистая эстетика. Сфокусировано по центру. Рядом, очень незаметно, несколько минималистичных акцентов, тонко раскрывающих его природную сущность или характерную свежесть, создавая ощущение тепла и домашнего уюта.", name, text)
}

type catalogueImageEntry struct {
	ID          int64
	Name        string
	Description string
}

func sortCatalogueImageEntries(entries []catalogueImageEntry) {
	sort.Slice(entries, func(i int, j int) bool { return entries[i].ID < entries[j].ID })
}
