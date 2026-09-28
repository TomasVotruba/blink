<?php

declare(strict_types=1);

namespace Fixture;

use PHPUnit\Framework\TestCase;

final class CrashTest extends TestCase
{
    public function testBeforeCrash(): void
    {
        $this->assertTrue(true);
    }

    public function testCrash(): void
    {
        echo "about to crash\n";

        exit(3);
    }
}
