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
- `-preload` - PHP file each worker runs once before forking, see below

Requires the `pcntl` extension, so Linux or macOS only.

## Preload

Every chunk of test files runs in a fresh child forked from a worker. If your tests boot something expensive, e.g. a DI container, each chunk boots it again. A preload file boots it once per worker instead, and every forked child starts with it ready:

```php
<?php

// blink-preload.php
require __DIR__ . '/tests/bootstrap.php';

// for Rector, see .github/preload/rector.php
MyTestCase::bootContainer();
```

```bash
blink -preload blink-preload.php
```

The file runs before PHPUnit reads its configuration, so `<php>` ini and env values from `phpunit.xml` are not set yet.

## How it works

- Go starts N long-running `php worker.php` processes. Each loads `vendor/autoload.php` and all PHPUnit classes once.
- Test files are split into chunks, about 3 per worker, of similar total duration based on `.blink-timings.json` from the previous run. Neighbour files stay together. A free worker takes the next chunk, slowest first.
- For each chunk, the worker forks a child that runs one normal PHPUnit run on all its files. The app or container boots once per chunk, not once per file. PHPUnit 9 accepts only one path, so there each file gets its own child.
- If a child crashes, files it did not reach run again in a new child. If nothing ran at all, e.g. on a syntax error, each file runs alone to find the broken one.
- Results come back via `--log-teamcity php://fd/3`, a pipe only Go reads, so test output can't break it. Go prints progress, failures and a summary.

## Benchmark

Measured on 2026-09-28 by the [Projects workflow](.github/workflows/projects.yaml), [run 36416588787](https://github.com/TomasVotruba/blink/actions/runs/36416588787):

| Project | PHPUnit | blink | Speedup | Tests | Skipped |
| --- | ---: | ---: | ---: | ---: | ---: |
| [laravel/framework](https://github.com/laravel/framework) `v13.33.0`, unit tests | 35.8s | 11.0s | **3.2x** | 12,359 | 87 |
| [rectorphp/rector-src](https://github.com/rectorphp/rector-src) `29e434b` | 32.7s | 13.2s | **2.4x** | 5,306 | 1 |

Setup:

- GitHub Actions `ubuntu-latest`, 4 CPUs
- PHP 8.4, PHPUnit 13.3, blink [`66c06f9`](https://github.com/TomasVotruba/blink/commit/66c06f9) with default options, so 4 workers
- `vendor/bin/phpunit` runs first, then blink without a previous `.blink-timings.json`
- Time is wall time of the whole command, including PHPUnit boot
- Laravel runs without `tests/Integration`, its integration tests share caches and files and are not safe to run in parallel
- Both runners report the same number of tests and skips, the workflow fails otherwise

Reproduce:

```bash
go build -o blink .

git clone https://github.com/rectorphp/rector-src.git
cd rector-src
git checkout 29e434b2065ae4a78a6b1eca4295620a5f6a2a5a
composer install

time vendor/bin/phpunit
time ../blink
```

For Laravel, check out `v13.33.0` of laravel/framework and exclude integration tests first:

```bash
sed 's|<directory suffix="Test.php">./tests</directory>|&<exclude>./tests/Integration</exclude>|' phpunit.xml.dist > phpunit-unit.xml

time vendor/bin/phpunit -c phpunit-unit.xml
time ../blink -c phpunit-unit.xml
```

## Tests

```bash
for v in 9 10 11 12 13; do (cd fixtures/phpunit-$v && composer install); done
go test ./...
```

`fixtures/tests` holds passing, failing, erroring, skipped, crashing and slow tests. It is shared by one small project per PHPUnit version.
