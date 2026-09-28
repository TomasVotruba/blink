<?php

declare(strict_types=1);

use Rector\Application\ApplicationFileProcessor;
use Rector\NodeTypeResolver\DependencyInjection\PHPStanServicesFactory;
use Rector\Testing\PHPUnit\AbstractLazyTestCase;

require __DIR__ . '/tests/bootstrap.php';

// builds the Rector and PHPStan containers once, forked test runs reuse them
final class BlinkPreload extends AbstractLazyTestCase
{
    public static function boot(): void
    {
        $rectorConfig = self::getContainer();
        $rectorConfig->make(ApplicationFileProcessor::class);
        $rectorConfig->get(PHPStanServicesFactory::class)->getContainer();
    }
}

BlinkPreload::boot();
