<?php

declare(strict_types=1);

namespace Fixture;

use PHPUnit\Framework\TestCase;

final class SkippedTest extends TestCase
{
    public function testSkipped(): void
    {
        $this->markTestSkipped('Not ready yet');
    }
}
