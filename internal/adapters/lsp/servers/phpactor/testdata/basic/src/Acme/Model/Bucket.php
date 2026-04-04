<?php

namespace Acme\Model;

class Bucket
{
    private string $value;

    public function __construct(string $value)
    {
        $this->value = $value;
    }

    public function describe(): string
    {
        return $this->value;
    }
}
