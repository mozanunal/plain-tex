package db

import (
	"database/sql"
	"fmt"
	"net/url"

	_ "modernc.org/sqlite"
)

func Open(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", dsn(path))
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(1)

	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}

	return db, nil
}

// dsn sets the pragmas through the connection string rather than a one-off Exec,
// so they hold for every connection the pool ever opens:
//   - foreign_keys(ON): the declared foreign keys are actually enforced.
//   - journal_mode(WAL): a crash mid-write cannot corrupt the database.
//   - busy_timeout: a writer waits instead of failing immediately under contention.
func dsn(path string) string {
	params := url.Values{}
	params.Add("_pragma", "foreign_keys(ON)")
	params.Add("_pragma", "journal_mode(WAL)")
	params.Add("_pragma", "busy_timeout(5000)")
	return "file:" + path + "?" + params.Encode()
}

func migrate(db *sql.DB) error {
	schema := `
	CREATE TABLE IF NOT EXISTS users (
		id TEXT PRIMARY KEY,
		email TEXT UNIQUE NOT NULL,
		password_hash TEXT NOT NULL,
		name TEXT,
		is_admin INTEGER NOT NULL DEFAULT 0,
		disabled INTEGER NOT NULL DEFAULT 0,
		session_version INTEGER NOT NULL DEFAULT 0,
		created TEXT DEFAULT (datetime('now')),
		updated TEXT DEFAULT (datetime('now'))
	) STRICT;

	CREATE TABLE IF NOT EXISTS projects (
		id TEXT PRIMARY KEY,
		user_id TEXT NOT NULL,
		name TEXT NOT NULL,
		compile_entry TEXT NOT NULL DEFAULT '',
		created TEXT DEFAULT (datetime('now')),
		updated TEXT DEFAULT (datetime('now')),
		FOREIGN KEY (user_id) REFERENCES users(id)
	) STRICT;

	CREATE TABLE IF NOT EXISTS comments (
		id TEXT PRIMARY KEY,
		project_id TEXT NOT NULL,
		file_path TEXT NOT NULL,
		start_line INTEGER NOT NULL,
		end_line INTEGER NOT NULL,
		body TEXT NOT NULL,
		snippet TEXT NOT NULL DEFAULT '',
		author_id TEXT NOT NULL,
		author_email TEXT NOT NULL,
		created TEXT DEFAULT (datetime('now')),
		updated TEXT DEFAULT (datetime('now')),
		FOREIGN KEY (project_id) REFERENCES projects(id),
		FOREIGN KEY (author_id) REFERENCES users(id),
		CHECK (start_line >= 1),
		CHECK (end_line >= start_line)
		) STRICT;

		CREATE INDEX IF NOT EXISTS idx_projects_user_id ON projects(user_id);

	CREATE TABLE IF NOT EXISTS project_members (
			project_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
		role TEXT NOT NULL CHECK (role IN ('reader', 'commenter', 'writer')),
		created TEXT DEFAULT (datetime('now')),
		updated TEXT DEFAULT (datetime('now')),
		PRIMARY KEY (project_id, user_id),
		FOREIGN KEY (project_id) REFERENCES projects(id),
		FOREIGN KEY (user_id) REFERENCES users(id)
		) STRICT;

		CREATE INDEX IF NOT EXISTS idx_project_members_user_id ON project_members(user_id);
		CREATE INDEX IF NOT EXISTS idx_project_members_project_id ON project_members(project_id);
		CREATE INDEX IF NOT EXISTS idx_comments_project_file ON comments(project_id, file_path, start_line);
		CREATE INDEX IF NOT EXISTS idx_comments_project_created ON comments(project_id, created);

		CREATE TABLE IF NOT EXISTS project_git_configs (
			project_id TEXT PRIMARY KEY,
			remote_url TEXT NOT NULL,
			branch TEXT NOT NULL,
			ssh_private_key_encrypted TEXT NOT NULL DEFAULT '',
			last_sync TEXT,
			created TEXT DEFAULT (datetime('now')),
			updated TEXT DEFAULT (datetime('now')),
			FOREIGN KEY (project_id) REFERENCES projects(id)
		) STRICT;

		CREATE INDEX IF NOT EXISTS idx_project_git_configs_updated ON project_git_configs(updated);

		CREATE TABLE IF NOT EXISTS user_git_keys (
			user_id TEXT PRIMARY KEY,
			public_key TEXT NOT NULL,
			private_key_encrypted TEXT NOT NULL,
			fingerprint TEXT NOT NULL,
			created TEXT DEFAULT (datetime('now')),
			updated TEXT DEFAULT (datetime('now')),
			FOREIGN KEY (user_id) REFERENCES users(id)
		) STRICT;

		CREATE INDEX IF NOT EXISTS idx_user_git_keys_updated ON user_git_keys(updated);
		`

	if _, err := db.Exec(schema); err != nil {
		return err
	}

	if err := ensureUsersAdminColumn(db); err != nil {
		return err
	}

	if err := ensureProjectsCompileEntryColumn(db); err != nil {
		return err
	}

	if err := ensureUsersColumn(db, "session_version", "ALTER TABLE users ADD COLUMN session_version INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := ensureUsersColumn(db, "disabled", "ALTER TABLE users ADD COLUMN disabled INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}

	if err := ensureFirstAdmin(db); err != nil {
		return err
	}

	return nil
}

func ensureUsersAdminColumn(db *sql.DB) error {
	exists, err := columnExists(db, "users", "is_admin")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = db.Exec("ALTER TABLE users ADD COLUMN is_admin INTEGER NOT NULL DEFAULT 0")
	return err
}

func ensureUsersColumn(db *sql.DB, column string, alter string) error {
	exists, err := columnExists(db, "users", column)
	if err != nil || exists {
		return err
	}
	_, err = db.Exec(alter)
	return err
}

func ensureProjectsCompileEntryColumn(db *sql.DB) error {
	exists, err := columnExists(db, "projects", "compile_entry")
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = db.Exec("ALTER TABLE projects ADD COLUMN compile_entry TEXT NOT NULL DEFAULT ''")
	return err
}

func ensureFirstAdmin(db *sql.DB) error {
	var adminCount int
	if err := db.QueryRow("SELECT COUNT(*) FROM users WHERE is_admin = 1").Scan(&adminCount); err != nil {
		return err
	}
	if adminCount > 0 {
		return nil
	}

	_, err := db.Exec(`
		UPDATE users
		SET is_admin = 1
		WHERE id = (
			SELECT id
			FROM users
			ORDER BY created ASC, id ASC
			LIMIT 1
		)
	`)
	return err
}

func columnExists(db *sql.DB, tableName string, columnName string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", tableName))
	if err != nil {
		return false, err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var columnType string
		var notNull int
		var defaultValue sql.NullString
		var primaryKey int

		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == columnName {
			return true, nil
		}
	}

	if err := rows.Err(); err != nil {
		return false, err
	}

	return false, nil
}
