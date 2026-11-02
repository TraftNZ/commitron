package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/johnstilia/commitron/pkg/ai"
	"github.com/johnstilia/commitron/pkg/config"
	"github.com/johnstilia/commitron/pkg/git"
	"github.com/spf13/cobra"
)

// Command-specific flags
var dryRun bool
var force bool

// generateCmd represents the generate command
var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate a commit message using AI",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Check if we're in a git repository
		if !git.IsGitRepo() {
			return fmt.Errorf("\033[1;31m❌ Not a git repository\033[0m")
		}

		// Use specified config file or default
		var cfg *config.Config
		var err error
		if configPath != "" {
			cfg, err = config.LoadConfigFromPath(configPath)
			if err != nil {
				return fmt.Errorf("\033[1;31m❌ Error loading configuration from %s: %w\033[0m", configPath, err)
			}
		} else {
			cfg, err = config.LoadConfig()
			if err != nil {
				return fmt.Errorf("\033[1;31m❌ Error loading configuration: %w\033[0m", err)
			}
		}

		return commitAll(cfg, ai.GenerateCommitMessage)
	},
}

// messageGenerator produces a commit message for the staged changes.
type messageGenerator func(cfg *config.Config, files []string, changes string) (string, error)

// commitAll commits changed submodules and then the current repository. Submodules go
// first because their new commits are what the parent commit records. It stops at the
// first failure so the parent never records a partial set; a rerun finds the finished
// submodules clean and resumes from there.
func commitAll(cfg *config.Config, generate messageGenerator) error {
	submodulesChanged := false
	if cfg.Commit.AutoCommitSubmodules {
		var err error
		submodulesChanged, err = commitSubmodules(cfg, generate)
		if err != nil {
			return err
		}
	}

	changed, err := commitRepository(cfg, generate)
	if err != nil {
		return err
	}
	if !changed && !submodulesChanged {
		return fmt.Errorf("\033[1;31m❌ No modified files found. Make some changes before running commitron\033[0m")
	}
	return nil
}

// commitSubmodules commits every changed submodule of the current repository, nested
// ones first, and reports whether any had changes to commit.
func commitSubmodules(cfg *config.Config, generate messageGenerator) (bool, error) {
	submodules, err := git.ListSubmodules()
	if err != nil {
		return false, fmt.Errorf("\033[1;31m❌ Error listing submodules: %w\033[0m", err)
	}

	anyChanged := false
	for _, submodule := range submodules {
		changed, err := commitSubmodule(cfg, submodule, generate)
		if err != nil {
			return anyChanged, err
		}
		anyChanged = anyChanged || changed
	}
	return anyChanged, nil
}

// commitSubmodule runs the commit flow inside one submodule, returning to the current
// directory afterwards.
func commitSubmodule(cfg *config.Config, dir string, generate messageGenerator) (changed bool, err error) {
	previous, err := os.Getwd()
	if err != nil {
		return false, fmt.Errorf("\033[1;31m❌ Error getting current directory: %w\033[0m", err)
	}
	if err := os.Chdir(dir); err != nil {
		return false, fmt.Errorf("\033[1;31m❌ Error entering submodule %s: %w\033[0m", dir, err)
	}
	defer func() {
		if restoreErr := os.Chdir(previous); restoreErr != nil && err == nil {
			err = fmt.Errorf("\033[1;31m❌ Error returning to %s: %w\033[0m", previous, restoreErr)
		}
	}()

	nestedChanged, err := commitSubmodules(cfg, generate)
	if err != nil {
		return nestedChanged, err
	}

	pending, err := git.HasUncommittedChanges()
	if err != nil {
		return nestedChanged, fmt.Errorf("\033[1;31m❌ Error checking submodule %s: %w\033[0m", dir, err)
	}
	if !pending {
		return nestedChanged, nil
	}

	// A commit on a detached HEAD belongs to no branch, so it is easily lost and cannot
	// be pushed by name. Leaving the changes uncommitted keeps them visible instead.
	detached, err := git.IsDetachedHead()
	if err != nil {
		return nestedChanged, fmt.Errorf("\033[1;31m❌ Error checking branch of submodule %s: %w\033[0m", dir, err)
	}
	if detached {
		fmt.Printf("\033[1;33m⚠️  Skipping submodule %s: it has changes but is not on a branch. Run `git switch <branch>` there, then rerun commitron\033[0m\n", dir)
		return nestedChanged, nil
	}

	fmt.Printf("\033[1;36m📂 Submodule %s\033[0m\n", dir)
	changed, err = commitRepository(cfg, generate)
	return changed || nestedChanged, err
}

// commitRepository stages and commits the changes in the current repository, reporting
// whether there were any. Having none is not an error, so callers can skip clean repositories.
func commitRepository(cfg *config.Config, generate messageGenerator) (bool, error) {
	// Stage tracked changes, plus new source files when enabled
	if err := git.StageAllModified(); err != nil {
		return false, fmt.Errorf("\033[1;31m❌ Error staging files: %w\033[0m", err)
	}
	if cfg.Commit.AutoStageNewSource {
		newFiles, err := git.StageNewSourceFiles()
		if err != nil {
			return false, fmt.Errorf("\033[1;31m❌ Error staging new source files: %w\033[0m", err)
		}
		for _, file := range newFiles {
			fmt.Printf("\033[1;33m➕ New file: %s\033[0m\n", file)
		}
	}

	stagedFiles, err := git.GetStagedFiles()
	if err != nil {
		return false, fmt.Errorf("\033[1;31m❌ Error getting staged files: %w\033[0m", err)
	}
	if len(stagedFiles) == 0 {
		return false, nil
	}
	fmt.Printf("\033[1;32m✓ Staged %d files\033[0m\n", len(stagedFiles))

	// Get changes content for context
	changes, err := git.GetStagedChanges()
	if err != nil {
		return true, fmt.Errorf("\033[1;31m❌ Error getting staged changes: %w\033[0m", err)
	}

	// Generate commit message using AI
	fmt.Println("\033[1;36m🤖 Analyzing changes...\033[0m")
	message, err := generate(cfg, stagedFiles, changes)
	if err != nil {
		return true, fmt.Errorf("\033[1;31m❌ Error generating commit message: %w\033[0m", err)
	}

	// In dry run mode, just display the message without committing
	if dryRun {
		fmt.Println("\n\033[38;5;244m🔍 Dry run completed. No commit was created.\033[0m")
		return true, nil
	}

	// Create the commit with the confirmed message
	fmt.Print("\n\033[1;36m💾 Creating commit... \033[0m")
	if err := git.Commit(message); err != nil {
		fmt.Println("\033[1;31m❌ failed\033[0m")
		return true, fmt.Errorf("\033[1;31m❌ Error: %w\033[0m", err)
	}
	fmt.Println("\033[1;32m✓ complete\033[0m")

	return true, nil
}

// initCmd represents the init command
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new configuration file",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Determine config path
		var targetPath string
		if configPath != "" {
			targetPath = configPath
		} else {
			homeDir, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("\033[1;31m❌ Error getting home directory: %w\033[0m", err)
			}
			targetPath = filepath.Join(homeDir, ".commitronrc")
		}

		// Check if config file already exists
		if _, err := os.Stat(targetPath); err == nil && !force {
			return fmt.Errorf("\033[1;31m❌ Configuration file already exists at %s (use --force to overwrite)\033[0m", targetPath)
		}

		// Create example config
		if err := config.SaveExampleConfig(targetPath); err != nil {
			return fmt.Errorf("\033[1;31m❌ Error creating configuration file: %w\033[0m", err)
		}

		fmt.Println("\n\033[1;32m✓ Configuration Ready\033[0m")
		fmt.Printf("\n  📁 File created at: \033[38;5;76m%s\033[0m\n", targetPath)
		fmt.Println("\n  \033[38;5;252mEdit this file to configure your AI provider and settings.\033[0m")
		return nil
	},
}

// versionCmd represents the version command
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Show the version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("\n\033[1;36mcommitron v0.1.16\033[0m")
		fmt.Println("\n  \033[38;5;252m🤖 AI-powered commit message generator\033[0m")
		fmt.Println("\n  \033[38;5;244mBuilt with ❤️ using Go\033[0m")
	},
}

func init() {
	// Add flags to generate command
	generateCmd.Flags().BoolVarP(&dryRun, "dry-run", "d", false, "Preview the commit message without creating a commit")

	// Add flags to init command
	initCmd.Flags().BoolVarP(&force, "force", "f", false, "Overwrite existing configuration file")
}
