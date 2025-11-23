# _qwash_

**qwash** is a CLI tool designed to **identify and reduce bloat** in PostgreSQL
databases **without blocking writes** (unlike `VACUUM FULL`). It provides
detailed bloat estimation for tables, indexes, and TOAST data, and offers **safe
cleanup strategies** to reclaim disk space and improve performance.  

Additionally, *qwash* helps fine-tune **Autovacuum** settings by analyzing
real-world table activity and suggesting adjustments for better maintenance
efficiency.

## Features

- **Bloat Estimation:**
  - Identify bloat in **tables**, **indexes**, and **TOAST data**.
  - Compute **absolute** (size in bytes) and **relative** (percentage) bloat.
  - Prioritize cleanup based on impact.

- **Non-Blocking Bloat Reduction:**
  - Reduce bloat **without table rewrites** (`VACUUM FULL` alternative).
  - Works incrementally, minimizing locking and system impact.
  - Inspired by `pg_compacttable`, but optimized in Go.

- **Autovacuum Tuning Assistance:**
  - Analyze bloat trends and vacuum efficiency.
  - Suggest **Autovacuum settings** tailored to table activity.
  - Identify tables requiring **manual maintenance**.

- **Flexible Execution:**
  - Run in **dry-run mode** to preview changes.
  - Select specific tables, schemas, or databases.
  - Control cleanup intensity (**conservative** vs **aggressive** modes).

- **Performance-Oriented:**
  - Native **multithreading** for faster execution.
  - Minimal overhead to avoid disrupting production workloads.

---

## Installation

Clone the repository and build the binary:

```sh
git clone https://github.com/yourusername/qwash.git
cd qwash
go build -o bin/qwash
```

---

## Usage

### Estimate Bloat  
```sh
./bin/qwash --estimate --dbname mydb
```

### Reduce Bloat (Safe Mode)  
```sh
./bin/qwash --clean --dbname mydb --mode conservative
```

### Reduce Bloat (Aggressive Mode)  
```sh
./bin/qwash --clean --dbname mydb --mode aggressive
```

### Dry Run (Preview Changes)  
```sh
./bin/qwash --estimate --clean --dry-run
```

### Get Autovacuum Recommendations  
```sh
./bin/qwash --autovacuum-tuning --dbname mydb
```

For more details, run:
```sh
./bin/qwash --help
```

---

## Contributing

Please see [CONTRIBUTING.md](CONTRIBUTING.md) for details on our code of conduct
and the process for submitting pull requests.

---

## License

This project is licensed under the terms of the PostgreSQL License. See
[LICENSE.md](LICENSE.md) for details.