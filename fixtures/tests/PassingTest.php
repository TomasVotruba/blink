<?php

declare(strict_types=1);

namespace Fixture;

use PHPUnit\Framework\Attributes\DataProvider;
use PHPUnit\Framework\TestCase;

final class PassingTest extends TestCase
{
    public function testTrue(): void
    {
        $this->assertTrue(true);
    }

    public function testEquals(): void
    {
        $this->assertSame('a', 'a');
    }

    /**
     * @dataProvider provideNumbers
     */
    #[DataProvider('provideNumbers')]
    public function testAdd(int $a, int $b, int $expected): void
    {
        $this->assertSame($expected, $a + $b);
    }

    public static function provideNumbers(): iterable
    {
        yield [1, 1, 2];
        yield [2, 3, 5];
        yield 'named' => [0, 0, 0];
    }
}
