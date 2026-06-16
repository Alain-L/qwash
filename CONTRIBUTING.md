# Contributing to _qwash_

Thank you for considering contributing to **qwash**! Contributions that improve
functionality, documentation, performance, or developer experience are highly
appreciated. Below are some guidelines to help you get started.

## Code of Conduct

Please review our [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) to understand the
expected behavior within our community.

## How to Contribute

**qwash** tries to balance pragmatism and good engineering practices. The
goal isn't perfection, but consistency: keep the code readable, write clear
commits, add tests when it makes sense, and update the docs if behavior
changes.

### 1. Fork the Repository
Create your own fork of the project on GitHub.

### 2. Create a Feature Branch
Use a **descriptive** branch name, such as:
- `feature/improve-parallel-processing`
- `bugfix/fix-bloat-calculation`

### 3. Write Your Code
- Keep the code consistent with what's already there.
- Add tests when relevant.
- Add comments or documentation where it helps understanding.

### 4. Commit Your Changes
- Write **clear** and **concise** commit messages.
- Follow a structured format to describe changes accurately.

### 5. Submit a Pull Request
- Open a pull request (PR) to the main repository.
- Provide a comprehensive description of your changes, their necessity, and
  reference any relevant issues.

## Reporting Issues

If you encounter a bug or have a feature request, please open an issue on
GitHub. Include as much detail as possible:
- Steps to reproduce
- Expected vs. actual behavior
- Environment details (OS, PostgreSQL version, Go version, etc.)

## Additional Notes

- **Testing:**
  The integration tests need a reachable PostgreSQL. The simplest way, from a
  clean clone, is a throwaway container:
  ```sh
  make test-db      # spins up PostgreSQL, runs the suite, tears it down
  ```
  Or against your own instance (via the standard PG* variables):
  ```sh
  make test         # builds the binary first, then `go test ./...`
  make lint         # gofmt -s, go vet, staticcheck (the CI gates)
  ```
- **Documentation:**
  If your changes impact usage, update the documentation accordingly.

Thank you for helping make _qwash_ a better project!
