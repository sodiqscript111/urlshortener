package services

import (
	"context"
	"errors"
	"math/rand"
	"net/url"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
	"urlshorter/internal/repository"
	"urlshorter/models"
	"urlshorter/utils"
)

type L1Cache interface {
	Get(key string) (string, bool)
	Set(key string, url string, ttl time.Duration)
}

type LinkService interface {
	Shorten(ctx context.Context, originalURL string) (string, error)
	Resolve(ctx context.Context, code string) (string, error)
	StartWorkers(ctx context.Context)
}

type ServiceConfig struct {
	FreshTTL time.Duration
	StaleTTL time.Duration
	Beta     float64
}

type linkService struct {
	linkRepo   repository.LinkRepository
	cacheRepo  repository.CacheRepository
	l1Cache    L1Cache
	group      singleflight.Group
	clickQueue chan string
	config     ServiceConfig
	workerOnce sync.Once
}

func NewLinkService(linkRepo repository.LinkRepository, cacheRepo repository.CacheRepository, l1 L1Cache, cfg ...ServiceConfig) LinkService {
	config := ServiceConfig{
		FreshTTL: 10 * time.Minute,
		StaleTTL: 30 * time.Minute,
		Beta:     0.8,
	}
	if len(cfg) > 0 {
		config = cfg[0]
	}

	return &linkService{
		linkRepo:   linkRepo,
		cacheRepo:  cacheRepo,
		l1Cache:    l1,
		clickQueue: make(chan string, 100000),
		config:     config,
	}
}

func (s *linkService) Shorten(ctx context.Context, rawURL string) (string, error) {
	parsed, err := url.ParseRequestURI(rawURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("invalid url")
	}

	code, err := utils.CryptoRandomString(10)
	if err != nil {
		return "", err
	}

	link := &models.Link{
		OriginalURL: rawURL,
		ShortCode:   code,
		Clicks:      0,
	}

	if err := s.linkRepo.Create(ctx, link); err != nil {
		return "", err
	}

	now := time.Now().Unix()
	freshTTL := s.config.FreshTTL
	if s.config.Beta > 0 {
		freshTTL = time.Duration(float64(s.config.FreshTTL) * (1 - s.config.Beta*rand.Float64()))
	}
	entry := &repository.CacheEntry{
		URL:        rawURL,
		FreshUntil: now + int64(freshTTL.Seconds()),
		StaleUntil: now + int64((freshTTL + s.config.StaleTTL).Seconds()),
	}

	if s.l1Cache != nil {
		s.l1Cache.Set("slug:"+code, rawURL, 30*time.Second)
	}
	_ = s.cacheRepo.Set(ctx, "slug:"+code, entry)

	return code, nil
}

func (s *linkService) Resolve(ctx context.Context, code string) (string, error) {
	key := "slug:" + code

	if s.l1Cache != nil {
		if val, ok := s.l1Cache.Get(key); ok {
			s.recordClick(code)
			return val, nil
		}
	}

	now := time.Now().Unix()
	entry, err := s.cacheRepo.Get(ctx, key)
	if err == nil && entry != nil {
		if s.l1Cache != nil {
			s.l1Cache.Set(key, entry.URL, 30*time.Second)
		}
		s.recordClick(code)

		if now < entry.FreshUntil {
			return entry.URL, nil
		}

		if now < entry.StaleUntil {
			go func() {
				_, _, _ = s.group.Do(key, func() (interface{}, error) {
					return s.refresh(context.Background(), code, key, entry.URL)
				})
			}()
			return entry.URL, nil
		}
	}

	resolvedURL, err := s.refresh(ctx, code, key, "")
	if err != nil {
		return "", err
	}

	s.recordClick(code)
	return resolvedURL, nil
}

func (s *linkService) refresh(ctx context.Context, code string, key string, staleURL string) (string, error) {
	val, err, _ := s.group.Do(key, func() (interface{}, error) {
		entry, err := s.cacheRepo.Get(ctx, key)
		if err == nil && entry != nil && time.Now().Unix() < entry.FreshUntil {
			return entry.URL, nil
		}

		link, err := s.linkRepo.FindByCode(ctx, code)
		if err != nil {
			if staleURL != "" {
				return staleURL, nil
			}
			return "", err
		}

		now := time.Now().Unix()
		freshTTL := s.config.FreshTTL
		if s.config.Beta > 0 {
			freshTTL = time.Duration(float64(s.config.FreshTTL) * (1 - s.config.Beta*rand.Float64()))
		}
		newEntry := &repository.CacheEntry{
			URL:        link.OriginalURL,
			FreshUntil: now + int64(freshTTL.Seconds()),
			StaleUntil: now + int64((freshTTL + s.config.StaleTTL).Seconds()),
		}

		if s.l1Cache != nil {
			s.l1Cache.Set(key, link.OriginalURL, 30*time.Second)
		}
		_ = s.cacheRepo.Set(ctx, key, newEntry)

		return link.OriginalURL, nil
	})

	if err != nil {
		if staleURL != "" {
			return staleURL, nil
		}
		return "", err
	}
	return val.(string), nil
}

func (s *linkService) recordClick(code string) {
	select {
	case s.clickQueue <- code:
	default:
	}
}

func (s *linkService) StartWorkers(ctx context.Context) {
	s.workerOnce.Do(func() {
		go s.clickBufferWorker(ctx)
		go s.outboxWorker(ctx)
	})
}

func (s *linkService) clickBufferWorker(ctx context.Context) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	var batch []interface{}
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = s.cacheRepo.PushClickBatch(ctx, batch)
		batch = batch[:0]
	}

	for {
		select {
		case <-ctx.Done():
			flush()
			return
		case code := <-s.clickQueue:
			batch = append(batch, code)
			if len(batch) >= 500 {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func (s *linkService) outboxWorker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		codes, err := s.cacheRepo.PopClickBatch(ctx, 1000)
		if err != nil || len(codes) == 0 {
			time.Sleep(500 * time.Millisecond)
			continue
		}

		clickCounts := make(map[string]int)
		for _, code := range codes {
			clickCounts[code]++
		}

		_ = s.cacheRepo.IncrementClicks(ctx, clickCounts)

		for code, count := range clickCounts {
			_ = s.linkRepo.IncrementClicks(ctx, code, count)
		}

		time.Sleep(500 * time.Millisecond)
	}
}
