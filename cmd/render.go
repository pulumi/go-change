// Copyright 2016-2022, Pulumi Corporation.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/aaronfriel/go-change/internal/changelog"
	"github.com/goccy/go-yaml"
	"github.com/google/go-github/v47/github"
	"github.com/spf13/cobra"
	"golang.org/x/oauth2"
)

func newRenderCmd() *cobra.Command {
	var configFilename string
	var inDir string
	var version string
	var format string
	var useGitHub bool
	var filterSinceCommit string
	var filterOpenPrNumber int

	var renderCmd = &cobra.Command{
		Use:   "render",
		Short: "Renders changelog entries to text",
		Long: `Renders changelog entries to text, defaulting to reading from changelog/pending ` +
			`and rendering to a markdown template`,
		RunE: func(cmd *cobra.Command, args []string) error {
			configData, err := os.ReadFile(configFilename)
			if err != nil {
				return fmt.Errorf("unable to read config file at path %v: %w", configFilename, err)
			}
			var config changelog.Config
			err = yaml.Unmarshal(configData, &config)
			if err != nil {
				return fmt.Errorf("unable to parse config file: %w", err)
			}
			if config.Template.Template == nil {
				config.Template = changelog.RenderTemplate{Template: changelog.DefaultTemplate}
			}

			// If we're previewing the changelog in a PR, there may be multiple open PRs containing the commit(s)
			// modifying this file. Only show open PRs related to the current commit.
			fullChangelog, err := extractChangelog(&config, filepath.Clean(inDir), filterSinceCommit, filterOpenPrNumber)
			if err != nil {
				return err
			}

			var date = time.Now().UTC().Format("2006-01-02")

			if format == "template" {
				buf, err := fullChangelog.Template(&config, version, date)
				if err != nil {
					return err
				}
				fmt.Print(buf.String())
			} else if format == "conventional" {
				var buf strings.Builder
				for i, v := range fullChangelog.Entries {
					if i > 0 {
						buf.WriteString("\n")
					}
					buf.WriteString(v.Conventional())
				}
				fmt.Print(buf.String())
			}

			return nil
		},
	}

	renderCmd.Flags().SortFlags = false
	renderCmd.Flags().StringVarP(&configFilename, "config", "c", "changelog/config.yaml", "Config file")
	renderCmd.Flags().StringVarP(&format, "format", "f", "template", "Format")
	renderCmd.Flags().StringVarP(&inDir, "input", "i", "changelog/pending", "Input directory")
	renderCmd.Flags().StringVarP(&version, "version", "v", "", "Version")
	renderCmd.Flags().BoolVarP(&useGitHub, "github", "", true, "Use GitHub to resolve pull request numbers")
	renderCmd.Flags().StringVarP(&filterSinceCommit, "filter-since-commit", "", "", "Filter changes to render to those since a given commitish")
	renderCmd.Flags().IntVarP(&filterOpenPrNumber, "filter-open-pr-number", "", 0, "Filter PR number detection to a particular open PR number.")

	return renderCmd
}

// extractChangelog crawls a directory and extracts the changelog entries from each applicable file,
// returning a merged changelog.
func extractChangelog(config *changelog.Config, dir string, filterSinceCommit string, filterOpenPrNumber int) (*changelog.Changelog, error) {
	owner, repo, err := config.GetGitHubRepository()
	if err != nil {
		return nil, err
	}

	inputFs := os.DirFS(dir)
	fullChangelog := &changelog.Changelog{}
	client := getClient(config)

	err = fs.WalkDir(inputFs, ".", func(path string, d fs.DirEntry, err error) error {
		if path == "." {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		if filepath.Ext(path) == ".yaml" {
			content, err := fs.ReadFile(inputFs, path)
			if err != nil {
				return nil
			}

			var entry changelog.Changelog
			err = yaml.Unmarshal(content, &entry)
			if err != nil {
				return fs.SkipDir
			}

			commitRange := "HEAD"
			if filterSinceCommit != "" {
				commitRange = filterSinceCommit + "..HEAD"
			}
			cmd := exec.Command("git", "rev-list", commitRange, filepath.Join(dir, path))
			revlistOutput, err := cmd.Output()
			if err != nil {
				return nil
			}
			output := strings.TrimSpace(string(revlistOutput))
			revlist := strings.Split(output, "\n")

			if filterSinceCommit != "" && (len(output) == 0 || len(revlist) == 0) {
				return nil
			}

			pullRequests := map[int]struct{}{}

			for _, v := range revlist {
				prs, _, err := client.PullRequests.ListPullRequestsWithCommit(context.TODO(), owner, repo, v, &github.PullRequestListOptions{})
				if err != nil {
					return nil
				}
				for _, pr := range prs {

					if filterOpenPrNumber != 0 && pr.GetState() == "open" && pr.GetNumber() != filterOpenPrNumber {
						continue
					}
					pullRequests[pr.GetNumber()] = struct{}{}
				}
			}

			for _, c := range entry.Entries {
				var prs []int
				for _, num := range c.GitHubMeta.PullRequestNumbers {
					if _, has := pullRequests[num]; has {
						// skip, the shared map has this entry already
					} else {
						prs = append(prs, num)
					}
				}

				for num := range pullRequests {
					prs = append(prs, num)
				}
				c.GitHubMeta.PullRequestNumbers = prs
			}

			fullChangelog = fullChangelog.Merge(entry)
		}

		return nil
	})
	if err != nil {
		return nil, err
	}
	return fullChangelog, nil
}

// getClient returns a GitHub client, using the access token from the config if available.
func getClient(config *changelog.Config) *github.Client {
	if accessToken := config.GetGitHubAccessToken(); accessToken != "" {
		ctx := context.Background()
		ts := oauth2.StaticTokenSource(
			&oauth2.Token{AccessToken: accessToken},
		)
		tc := oauth2.NewClient(ctx, ts)

		return github.NewClient(tc)
	}
	return github.NewClient(nil)
}

func init() {
	rootCmd.AddCommand(newRenderCmd())
}
