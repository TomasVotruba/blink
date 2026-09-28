<?php

declare(strict_types=1);

// blink worker: loads the autoloader once, then forks one PHPUnit run per test file read from stdin.
// Test results go to fd 3 as TeamCity messages, followed by "##blink[done ...]" once the child exits.

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
    $testFile = rtrim($line, "\n");

    $pid = pcntl_fork();
    if ($pid === -1) {
        fwrite($events, "##blink[done exit='255' signal='0']\n");
        continue;
    }

    if ($pid === 0) {
        $_SERVER['argv'] = ['phpunit', '--log-teamcity', 'php://fd/3', ...$phpunitArgs, $testFile];
        $_SERVER['argc'] = count($_SERVER['argv']);

        // PHPUnit 10+
        if (class_exists(PHPUnit\TextUI\Application::class)) {
            exit((new PHPUnit\TextUI\Application())->run($_SERVER['argv']));
        }

        // PHPUnit 9
        exit(PHPUnit\TextUI\Command::main(false));
    }

    pcntl_waitpid($pid, $status);

    $exitCode = pcntl_wifexited($status) ? pcntl_wexitstatus($status) : 255;
    $signal = pcntl_wifsignaled($status) ? pcntl_wtermsig($status) : 0;

    // marks end of this file's output on stdout
    fwrite(STDOUT, "\0blink-eof\0\n");
    fwrite($events, "##blink[done exit='{$exitCode}' signal='{$signal}']\n");
}
