<?php

declare(strict_types=1);

namespace Fixture\Slow;

use PHPUnit\Framework\TestCase;

final class SlowOneTest extends TestCase
{
    public function testSlow(): void
    {
        usleep(300_000);

        $this->assertTrue(true);
    }
}
