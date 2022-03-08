package detect

import (
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gitleaks/go-gitdiff/gitdiff"
	"github.com/rs/zerolog/log"
	"github.com/zricethezav/gitleaks/v8/config"
	"github.com/zricethezav/gitleaks/v8/report"
)

// FromGit accepts a gitdiff.File channel (structure output from `git log -p`) and a configuration
// struct. Files from the gitdiff.File channel are then checked against each rule in the configuration to
// check for secrets. If any secrets are found, they are added to the list of findings.
func FromGit(files <-chan *gitdiff.File, cfg config.Config, outputOptions Options) []report.Finding {
	var findings []report.Finding
	mu := sync.Mutex{}
	wg := sync.WaitGroup{}
	concurrentGoroutines := make(chan struct{}, MAXGOROUTINES)
	commitMap := make(map[string]bool)

	detector := NewDetector(cfg, outputOptions.Verbose, outputOptions.Redact)

	for f := range files {
		// keep track of commits for logging
		if f.PatchHeader != nil {
			commitMap[f.PatchHeader.SHA] = true
		}
		start := time.Now()
		wg.Add(1)
		concurrentGoroutines <- struct{}{}
		go func(f *gitdiff.File) {
			defer func() {
				wg.Done()
				<-concurrentGoroutines
			}()
			if f.IsBinary {
				return
			}

			if f.IsDelete {
				return
			}

			commitSHA := ""

			// Check if commit is allowed
			if f.PatchHeader != nil {
				commitSHA = f.PatchHeader.SHA
				if cfg.Allowlist.CommitAllowed(f.PatchHeader.SHA) {
					return
				}
			}

			for _, tf := range f.TextFragments {
				if f.TextFragments == nil {
					// TODO fix this in gitleaks gitdiff fork
					// https://github.com/gitleaks/gitleaks/issues/11
					continue
				}

				for _, fi := range detector.Detect([]byte(tf.Raw(gitdiff.OpAdd)), f.NewName, commitSHA) {
					// don't add to start/end lines if finding is from a file only rule
					if !strings.HasPrefix(fi.Match, "file detected") {
						fi.StartLine += int(tf.NewPosition)
						fi.EndLine += int(tf.NewPosition)
					}
					if f.PatchHeader != nil {
						fi.Commit = f.PatchHeader.SHA
						fi.Message = f.PatchHeader.Message()
						if f.PatchHeader.Author != nil {
							fi.Author = f.PatchHeader.Author.Name
							fi.Email = f.PatchHeader.Author.Email
						}
						fi.Date = f.PatchHeader.AuthorDate.UTC().Format(time.RFC3339)
					}

					if outputOptions.Redact {
						fi.Redact()
					}

					if outputOptions.Verbose {
						printFinding(fi)
					}
					mu.Lock()
					findings = append(findings, fi)
					mu.Unlock()

				}
			}
		}(f)
		fileTimings.Add(filepath.Ext(f.NewName), time.Since(start))
	}

	wg.Wait()
	timings.Report()
	// fileTimings.Report()
	log.Debug().Msgf("%d commits scanned. Note: this number might be smaller than expected due to commits with no additions", len(commitMap))
	return findings
}
