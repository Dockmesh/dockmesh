package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/dockmesh/dockmesh/internal/auth"
)

// runAdminCmd handles `dockmesh admin <subcommand>`. Bootstrap recovery
// + offline user creation without going through the HTTP API.
func runAdminCmd(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: dockmesh admin <create|reset-password|unlock|list-users> [flags]")
		os.Exit(2)
	}
	switch args[0] {
	case "create":
		if err := adminCreate(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "admin create:", err)
			os.Exit(1)
		}
	case "reset-password":
		if err := adminResetPassword(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "admin reset-password:", err)
			os.Exit(1)
		}
	case "unlock":
		if err := adminUnlock(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "admin unlock:", err)
			os.Exit(1)
		}
	case "list-users":
		if err := adminListUsers(args[1:]); err != nil {
			fmt.Fprintln(os.Stderr, "admin list-users:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown admin subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

// adminUnlock clears a user's lockout state without touching the stored
// password. Lockouts auto-expire via policy.LockoutDurationMins, but
// homelab single-admin installs sometimes want to bypass the wait —
// especially when the admin KNOWS the password but got tripped by
// browser autofill replaying a stale credential.
func adminUnlock(args []string) error {
	fs := flag.NewFlagSet("admin unlock", flag.ExitOnError)
	userFlag := fs.String("user", "", "username or user id (required)")
	_ = fs.Parse(args)

	if *userFlag == "" {
		return fmt.Errorf("--user is required")
	}

	_, database, err := loadCLIDB()
	if err != nil {
		return err
	}
	defer database.Close()

	var userID string
	row := database.QueryRow(`SELECT id FROM users WHERE id = ? OR username = ?`, *userFlag, *userFlag)
	if err := row.Scan(&userID); err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}

	// Wipe both fields — failed_login_attempts resets the counter so
	// the NEXT wrong attempt starts at 1, not N+1 which would re-lock
	// after one mistake.
	res, err := database.Exec(
		`UPDATE users SET locked_until = NULL, failed_login_attempts = 0 WHERE id = ?`,
		userID,
	)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows == 0 {
		return fmt.Errorf("no user updated (id %s)", userID)
	}
	fmt.Printf("unlocked user %s (id=%s) — failed-attempt counter reset\n", *userFlag, userID)
	return nil
}

func adminCreate(args []string) error {
	fs := flag.NewFlagSet("admin create", flag.ExitOnError)
	username := fs.String("username", "", "username (required)")
	email := fs.String("email", "", "email (optional)")
	role := fs.String("role", "viewer", "role (admin|editor|viewer or custom role id)")
	password := fs.String("password", "", "password (omit to be prompted, or pipe from stdin)")
	_ = fs.Parse(args)

	if *username == "" {
		return fmt.Errorf("--username is required")
	}

	pw := *password
	if pw == "" {
		var err error
		pw, err = readPassword(fmt.Sprintf("password for %s: ", *username))
		if err != nil {
			return err
		}
		if pw == "" {
			return fmt.Errorf("empty password")
		}
	}

	_, database, err := loadCLIDB()
	if err != nil {
		return err
	}
	defer database.Close()

	authSvc := auth.NewService(database, make([]byte, 32))
	user, err := authSvc.CreateUser(context.Background(), *username, *email, pw, *role)
	if err != nil {
		return err
	}
	fmt.Printf("created user: id=%s username=%s role=%s\n", user.ID, user.Username, user.Role)
	return nil
}

func adminResetPassword(args []string) error {
	fs := flag.NewFlagSet("admin reset-password", flag.ExitOnError)
	userFlag := fs.String("user", "", "username or user id (required)")
	password := fs.String("password", "", "new password (omit to be prompted, or pipe from stdin)")
	_ = fs.Parse(args)

	if *userFlag == "" {
		return fmt.Errorf("--user is required")
	}

	pw := *password
	if pw == "" {
		var err error
		pw, err = readPassword(fmt.Sprintf("new password for %s: ", *userFlag))
		if err != nil {
			return err
		}
		if pw == "" {
			return fmt.Errorf("empty password")
		}
	}

	_, database, err := loadCLIDB()
	if err != nil {
		return err
	}
	defer database.Close()

	// Resolve the target: accept either username or user id.
	var userID string
	row := database.QueryRow(`SELECT id FROM users WHERE id = ? OR username = ?`, *userFlag, *userFlag)
	if err := row.Scan(&userID); err != nil {
		return fmt.Errorf("lookup user: %w", err)
	}

	authSvc := auth.NewService(database, make([]byte, 32))
	if err := authSvc.ChangePassword(context.Background(), userID, pw); err != nil {
		return err
	}
	fmt.Printf("password reset for user %s (id=%s)\n", *userFlag, userID)
	return nil
}

func adminListUsers(args []string) error {
	_ = flag.NewFlagSet("admin list-users", flag.ExitOnError).Parse(args)

	_, database, err := loadCLIDB()
	if err != nil {
		return err
	}
	defer database.Close()

	authSvc := auth.NewService(database, make([]byte, 32))
	users, err := authSvc.ListUsers(context.Background())
	if err != nil {
		return err
	}
	if len(users) == 0 {
		fmt.Println("(no users)")
		return nil
	}
	fmt.Printf("%-40s  %-20s  %-10s  %-30s  %s\n", "ID", "USERNAME", "ROLE", "EMAIL", "MFA")
	for _, u := range users {
		mfa := "no"
		if u.MFAEnabled {
			mfa = "yes"
		}
		email := u.Email
		if email == "" {
			email = "-"
		}
		fmt.Printf("%-40s  %-20s  %-10s  %-30s  %s\n", u.ID, u.Username, u.Role, email, mfa)
	}
	return nil
}

// trimFlags strips surrounding whitespace from all positional args so
// `--tags "a, b,c"` parses cleanly into ["a","b","c"]. Used by tag-style
// comma-joined flags on enroll create.
func splitCSV(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
