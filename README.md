# blink

Coding standard tool for any PHP version, from 5.3 up. Install it into legacy projects that can't require ECS, which needs PHP 7.2+.

```bash
composer require --dev tomasvotruba/blink
```

```bash
vendor/bin/blink src tests        # check
vendor/bin/blink --fix src tests  # fix
vendor/bin/blink --help
```

It runs the `blink` Go binary from [ECS](https://github.com/ecsphp/ecs), so it needs no PHP 7.2+ at all. Binaries are included for Linux and macOS, on x86_64 and ARM.

## Update binaries

```bash
build/sync-ecs.sh           # latest ECS release
build/sync-ecs.sh 13.3.23   # given version
```
