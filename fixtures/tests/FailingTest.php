<?php

declare(strict_types=1);

namespace Fixture;

use PHPUnit\Framework\TestCase;

final class FailingTest extends TestCase
{
    public function testPasses(): void
    {
        $this->assertTrue(true);
    }

    public function testFails(): void
    {
        $this->assertSame(1, 2);
    }
}
