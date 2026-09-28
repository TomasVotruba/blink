<?php

declare(strict_types=1);

namespace Fixture;

use PHPUnit\Framework\TestCase;
use RuntimeException;

final class ErrorTest extends TestCase
{
    public function testThrows(): void
    {
        throw new RuntimeException('Something broke');
    }
}
