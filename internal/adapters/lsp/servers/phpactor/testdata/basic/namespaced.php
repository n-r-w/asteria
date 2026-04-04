<?php

namespace Acme\Model;

use Countable;
use Stringable;

#[\Attribute(\Attribute::TARGET_CLASS | \Attribute::TARGET_METHOD)]
class Marker {}

interface BucketFormatter
{
    public function format(string $value): string;
}

trait BucketLabeling
{
    public const DEFAULT_PREFIX = 'bucket';

    public static function describeStatic(string $value): string
    {
        return self::DEFAULT_PREFIX . ':' . $value;
    }
}

enum BucketKind: string
{
    case Primary = 'primary';
    case Secondary = 'secondary';
}

class BaseBucket
{
    public function inheritedLabel(): string
    {
        return 'base';
    }
}

#[Marker]
class AdvancedBucket extends BaseBucket implements BucketFormatter, Countable, Stringable
{
    use BucketLabeling;

    public const KIND_PREFIX = 'kind';

    public function __construct(
        private readonly string $value,
        private ?BucketKind $kind = null,
    ) {}

    #[Marker]
    public function format(string $value): string
    {
        return self::describeStatic($value);
    }

    public function describe(): string
    {
        $closure = function (string $suffix): string {
            return $this->value . $suffix;
        };

        $worker = new class($closure) {
            public function __construct(private \Closure $formatter) {}

            public function run(string $suffix): string
            {
                return ($this->formatter)($suffix);
            }
        };

        return $worker->run('-' . ($this->kind?->value ?? 'none'));
    }

    public static function createTagged(string|Stringable $value, Countable&Stringable $tag): self
    {
        return new self((string) $value . ':' . $tag, BucketKind::Primary);
    }

    public function count(): int
    {
        return strlen($this->value);
    }

    public function __toString(): string
    {
        return $this->describe();
    }
}
