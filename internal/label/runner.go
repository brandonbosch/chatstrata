package label

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/brandonbosch/chatstrata/v2/internal/store"
)

// RunOptions controls execution, durability, and budget enforcement for RunPack.
type RunOptions struct {
	LabelsDir        string
	RedactionVersion string
	Concurrency      int
	MaxCostUSD       float64
	PricePerMTok     float64
	OnProgress       func(done, total int)
}

// RunStats summarizes a completed or partially completed label run.
type RunStats struct {
	RunID         string
	RunPath       string
	Model         string
	Labelled      int
	Failures      int
	InputTokens   int64
	BudgetStopped bool
	Errors        []string
}

type workResult struct {
	target Target
	res    AskResult
	err    error
}

// RunPack evaluates targets concurrently against backend, persists every
// completed answer to the durable sidecar log first, and upserts it into
// DuckDB. Partial progress is finalized into an immutable .jsonl.zst run even
// if cancelled, budget-capped, or halted by a fatal backend error.
func RunPack(ctx context.Context, s *store.Store, pack *Pack, backend Backend, targets []Target, opts RunOptions) (RunStats, error) {
	if len(targets) == 0 {
		return RunStats{}, nil
	}
	price := opts.PricePerMTok
	if price <= 0 {
		price = DefaultPricePerMTok
	}
	if opts.MaxCostUSD > 0 {
		estTokens := EstimateTokens(pack, targets)
		estCost := EstimateCostUSD(estTokens, price)
		if estCost > opts.MaxCostUSD {
			return RunStats{}, fmt.Errorf(
				"estimated cost $%.4f (%d targets, ~%d input tokens) exceeds --max-cost-usd $%.4f; lower --limit or raise --max-cost-usd",
				estCost, len(targets), estTokens, opts.MaxCostUSD)
		}
	}

	dir := opts.LabelsDir
	if dir == "" {
		dir = Dir(s.Path)
	}
	concurrency := opts.Concurrency
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(targets) {
		concurrency = len(targets)
	}

	header := RunHeader{
		RunID:            NewRunID(),
		Pack:             pack.Name,
		PackVersion:      pack.Version(),
		TargetKind:       pack.TargetKind,
		Backend:          backend.Name(),
		RedactionVersion: opts.RedactionVersion,
		StartedAt:        time.Now().UTC(),
	}
	writer, err := OpenRunWriter(dir, header)
	if err != nil {
		return RunStats{}, err
	}
	if err := UpsertRunHeader(ctx, s, header); err != nil {
		_, _ = writer.Finish(header)
		return RunStats{}, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	jobs := make(chan Target)
	results := make(chan workResult, concurrency)
	var wg sync.WaitGroup
	for range concurrency {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for t := range jobs {
				if runCtx.Err() != nil {
					return
				}
				res, err := backend.Ask(runCtx, t.State, pack.Questions)
				select {
				case results <- workResult{target: t, res: res, err: err}:
				case <-runCtx.Done():
					// Deliver completed answers even if another goroutine tripped cancel.
					if err == nil {
						results <- workResult{target: t, res: res}
					}
					return
				}
			}
		}()
	}

	go func() {
		defer close(jobs)
		for _, t := range targets {
			select {
			case jobs <- t:
			case <-runCtx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()

	stats := RunStats{RunID: header.RunID}
	var fatalErr error
	var writeErr error
	done := 0

	for r := range results {
		done++
		if r.err != nil {
			var fatal *FatalError
			if errors.As(r.err, &fatal) {
				if fatalErr == nil {
					fatalErr = r.err
					cancel()
				}
			} else if !errors.Is(r.err, context.Canceled) {
				stats.Failures++
				if len(stats.Errors) < 10 {
					stats.Errors = append(stats.Errors, fmt.Sprintf("%s: %s", r.target.ID, r.err))
				}
			}
			if opts.OnProgress != nil {
				opts.OnProgress(done, len(targets))
			}
			continue
		}

		rec := AnswerRecord{
			RunID:             header.RunID,
			TargetKind:        pack.TargetKind,
			TargetID:          r.target.ID,
			TargetFingerprint: r.target.Fingerprint,
			Model:             r.res.Model,
			InputTokens:       r.res.InputTokens,
			CreatedAt:         time.Now().UTC(),
			Answers:           r.res.Answers,
		}
		if stats.Model == "" && r.res.Model != "" {
			stats.Model = r.res.Model
			header.Model = r.res.Model
		}
		// Durable sidecar first, then DuckDB projection (ADR 0007).
		if err := writer.Append(rec); err != nil {
			writeErr = fmt.Errorf("write label log: %w", err)
			cancel()
			break
		}
		if _, err := UpsertAnswer(context.Background(), s, header, rec); err != nil {
			writeErr = fmt.Errorf("upsert label projection: %w", err)
			cancel()
			break
		}
		stats.Labelled++
		stats.InputTokens += r.res.InputTokens
		if opts.MaxCostUSD > 0 && EstimateCostUSD(stats.InputTokens, price) >= opts.MaxCostUSD {
			stats.BudgetStopped = true
			cancel()
		}
		if opts.OnProgress != nil {
			opts.OnProgress(done, len(targets))
		}
	}

	header.Model = stats.Model
	header.FinishedAt = time.Now().UTC()
	header.Targets = stats.Labelled
	header.Failures = stats.Failures
	header.InputTokens = stats.InputTokens

	runPath, finishErr := writer.Finish(header)
	stats.RunPath = runPath
	if err := UpsertRunHeader(context.Background(), s, header); err != nil && writeErr == nil {
		writeErr = err
	}

	if writeErr != nil {
		return stats, writeErr
	}
	if finishErr != nil {
		return stats, finishErr
	}
	if fatalErr != nil {
		return stats, fatalErr
	}
	if ctx.Err() != nil {
		return stats, ctx.Err()
	}
	return stats, nil
}
