<?php

declare(strict_types=1);

// blink worker: loads the autoloader once, then forks a PHPUnit run per line of tab separated test files from stdin.
// Test results go to fd 3 as TeamCity messages, followed by "##blink[done ...]" once the run is over.
// Before each test file the run sends "##blink[claim ...]" and waits for one byte on fd 4, "0" means another
// worker took the file, so the run stops there.

[, $autoloadFile] = $argv;
$phpunitArgs = array_slice($argv, 2);

if (!function_exists('pcntl_fork')) {
    fwrite(STDERR, 'blink requires the pcntl extension' . PHP_EOL);
    exit(1);
}

if (!ini_get('date.timezone')) {
    ini_set('date.timezone', 'UTC');
}

define('PHPUNIT_COMPOSER_INSTALL', $autoloadFile);
require $autoloadFile;

// load PHPUnit classes once here, so forked children don't have to compile them again
$classMapFile = dirname($autoloadFile) . '/composer/autoload_classmap.php';
if (is_file($classMapFile)) {
    $errorLevel = error_reporting(0);

    foreach (require $classMapFile as $class => $file) {
        if (!str_starts_with($class, 'PHPUnit\\') && !str_starts_with($class, 'SebastianBergmann\\')) {
            continue;
        }

        try {
            class_exists($class) || interface_exists($class) || trait_exists($class);
        } catch (Throwable) {
            // optional dependency missing, the class is simply not preloaded
        }
    }

    error_reporting($errorLevel);
}

$events = fopen('php://fd/3', 'wb');
fwrite($events, "##blink[ready]\n");

while (($line = fgets(STDIN)) !== false) {
    $testFiles = explode("\t", rtrim($line, "\n"));

    // PHPUnit 10+ runs all files in one process and claims them itself, PHPUnit 9 accepts only one path
    $isPhpunit9 = !class_exists(PHPUnit\TextUI\Application::class);
    $runs = $isPhpunit9 ? array_map(static fn (string $testFile): array => [$testFile], $testFiles) : [$testFiles];

    $exitCode = 0;
    $signal = 0;

    foreach ($runs as $runFiles) {
        if ($isPhpunit9 && !claim($runFiles[0])) {
            break;
        }

        [$runExitCode, $runSignal] = runInChild($runFiles, $phpunitArgs);
        $exitCode = max($exitCode, $runExitCode);
        $signal = $signal ?: $runSignal;
    }

    // marks end of this run's output on stdout
    fwrite(STDOUT, "\0blink-eof\0\n");
    fwrite($events, "##blink[done exit='{$exitCode}' signal='{$signal}']\n");
}

/**
 * @param string[] $testFiles
 * @param string[] $phpunitArgs
 * @return array{int, int} exit code and signal
 */
function runInChild(array $testFiles, array $phpunitArgs): array
{
    $pid = pcntl_fork();
    if ($pid === -1) {
        return [255, 0];
    }

    if ($pid === 0) {
        $_SERVER['argv'] = ['phpunit', '--log-teamcity', 'php://fd/3', ...$phpunitArgs, ...$testFiles];
        $_SERVER['argc'] = count($_SERVER['argv']);

        // PHPUnit 10+
        if (class_exists(PHPUnit\TextUI\Application::class)) {
            register_shutdown_function(reportSkippedClasses(...));
            PHPUnit\Event\Facade::instance()->registerSubscriber(new class implements PHPUnit\Event\TestSuite\StartedSubscriber {
                /** @var array<string, true> */
                private array $claimedFiles = [];

                public function notify(PHPUnit\Event\TestSuite\Started $event): void
                {
                    $testSuite = $event->testSuite();
                    if (!$testSuite->isForTestClass()) {
                        return;
                    }

                    $file = (string) (new ReflectionClass($testSuite->className()))->getFileName();
                    if (isset($this->claimedFiles[$file])) {
                        return;
                    }

                    if (!claim($file)) {
                        exit(0);
                    }

                    $this->claimedFiles[$file] = true;
                }
            });
            exit((new PHPUnit\TextUI\Application())->run($_SERVER['argv']));
        }

        // PHPUnit 9
        exit(PHPUnit\TextUI\Command::main(false));
    }

    pcntl_waitpid($pid, $status);

    return [
        pcntl_wifexited($status) ? pcntl_wexitstatus($status) : 255,
        pcntl_wifsignaled($status) ? pcntl_wtermsig($status) : 0,
    ];
}

function claim(string $file): bool
{
    static $events, $control;
    $events ??= fopen('php://fd/3', 'wb');
    $control ??= fopen('php://fd/4', 'rb');

    fwrite($events, sprintf("##blink[claim file='%s']\n", escapeTeamCity(realpath($file) ?: $file)));

    return fread($control, 1) === '1';
}

// TeamCity reports a skipped class, e.g. with a missing extension, as one message without the number of its tests
function reportSkippedClasses(): void
{
    try {
        $skippedEvents = PHPUnit\TestRunner\TestResult\Facade::result()->testSuiteSkippedEvents();
    } catch (Throwable) {
        return;
    }

    $events = fopen('php://fd/3', 'wb');
    foreach ($skippedEvents as $skippedEvent) {
        fwrite($events, sprintf(
            "##blink[classSkipped name='%s' message='%s' count='%d']\n",
            escapeTeamCity($skippedEvent->testSuite()->name()),
            escapeTeamCity($skippedEvent->message()),
            $skippedEvent->testSuite()->count(),
        ));
    }
}

function escapeTeamCity(string $value): string
{
    return str_replace(['|', "'", "\n", "\r", ']', '['], ['||', "|'", '|n', '|r', '|]', '|['], $value);
}
