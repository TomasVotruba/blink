# blink

Parallel PHPUnit runner written in Go. Works with PHPUnit 9 to 13 on PHP 8.1+.

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
- Test files are split into chunks, about 3 per worker, of similar total duration based on `.blink-timings.json` from the previous run. Neighbour files stay together. A free worker takes the next chunk, slowest first.
- For each chunk, the worker forks a child that runs one normal PHPUnit run on all its files. The app or container boots once per chunk, not once per file. PHPUnit 9 accepts only one path, so there each file gets its own child.
- If a child crashes, files it did not reach run again in a new child. If nothing ran at all, e.g. on a syntax error, each file runs alone to find the broken one.
- Results come back via `--log-teamcity php://fd/3`, a pipe only Go reads, so test output can't break it. Go prints progress, failures and a summary.

## Tests

```bash
for v in 9 10 11 12 13; do (cd fixtures/phpunit-$v && composer install); done
go test ./...
```

`fixtures/tests` holds passing, failing, erroring, skipped, crashing and slow tests. It is shared by one small project per PHPUnit version.
