<?php

namespace Acme\Consumer;

require_once __DIR__ . '/namespaced.php';

use Acme\Model\AdvancedBucket as ImportedBucket;
use Countable;
use Stringable;

final class BucketTag implements Countable, Stringable
{
    public function count(): int
    {
        return 1;
    }

    public function __toString(): string
    {
        return 'tag';
    }
}

function use_namespaced_bucket(): string
{
    $bucket = ImportedBucket::createTagged('value', new BucketTag());

    return ImportedBucket::describeStatic($bucket->describe()) . ':' . ImportedBucket::KIND_PREFIX;
}
