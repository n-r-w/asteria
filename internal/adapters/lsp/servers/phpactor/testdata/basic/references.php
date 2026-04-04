<?php

require_once __DIR__ . '/fixture.php';

function use_bucket(): string
{
    $left = new Bucket('primary');
    $right = make_bucket('secondary');

    return $left->describe() . $right->describe();
}
