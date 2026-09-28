# blink

Parallel PHPUnit runner written in Go. Works with PHPUnit 9 to 13.

```bash
go build -o blink .

cd /path/to/php-project
blink                          # test files from phpunit.xml(.dist) testsuites
blink tests/Unit               # *Test.php files in given paths
blink -j 4 -- --filter testFoo # options after "--" go to PHPUnit
```

Options:

- `-j` - number of workers, defaults to CPU count
- `-c` - PHPUnit config file, defaults to `phpunit.xml` or `phpunit.xml.dist`
- `-php` - PHP binary, defaults to `php`

Requires the `pcntl` extension, so Linux or macOS only.

## How it works

- Go starts N long-running `php worker.php` processes. Each loads `vendor/autoload.php` and all PHPUnit classes once.
- Test files go into one queue, slowest first based on `.blink-timings.json` from the previous run. A free worker takes the next file.
- For each file, the worker forks a child that runs a normal PHPUnit run on that file. Every file starts from the same clean, booted state, and a crash only kills the child.
- The child reports results with `--log-teamcity php://fd/3`, a pipe only Go reads, so test output can't break it. Go prints progress, failures and a summary.

## Tests

```bash
for v in 9 10 11 12 13; do (cd fixtures/phpunit-$v && composer install); done
go test ./...
```

`fixtures/tests` holds passing, failing, erroring, skipped, crashing and slow tests. It is shared by one small project per PHPUnit version.
