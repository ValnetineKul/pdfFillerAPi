CREATE UNIQUE INDEX IF NOT EXISTS users_login_lower_idx ON users (lower(login));
