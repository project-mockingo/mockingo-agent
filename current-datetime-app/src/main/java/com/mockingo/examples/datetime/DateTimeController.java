package com.mockingo.examples.datetime;

import java.time.Clock;
import java.time.ZonedDateTime;
import java.time.format.DateTimeFormatter;

import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.RestController;

@RestController
public class DateTimeController {

    private final Clock clock;

    public DateTimeController(Clock clock) {
        this.clock = clock;
    }

    @GetMapping({"/", "/datetime"})
    public DateTimeResponse currentDateTime() {
        String currentDateTime = ZonedDateTime.now(clock)
                .format(DateTimeFormatter.ISO_OFFSET_DATE_TIME);
        return new DateTimeResponse(currentDateTime, "UTC");
    }

    public record DateTimeResponse(String datetime, String timezone) {
    }
}
